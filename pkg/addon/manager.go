package addon

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// LauncherVersion is stamped into the addon log banner; main sets it from the
// build's Version so a pasted addon.log reveals which launcher build wrote it.
var LauncherVersion = "dev"

// Manager handles addon discovery, installation, and management.
type Manager struct {
	DataDir    string // ~/.local/share/neocron-launcher/addons/
	InstallDir string // game install directory
	Logger     *log.Logger

	mu sync.Mutex
}

// NewManager creates a new addon manager.
func NewManager(installDir string) *Manager {
	dataDir := DefaultDataDir()
	os.MkdirAll(dataDir, 0755)

	// Open log file for addon operations
	logPath := filepath.Join(dataDir, "addon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	var logger *log.Logger
	if err == nil {
		logger = log.New(logFile, "", log.LstdFlags)
	} else {
		logger = log.New(os.Stderr, "[addon] ", log.LstdFlags)
	}

	// Banner each session start so users pasting addon.log into bug reports
	// reveal which launcher build wrote the entries.
	logger.Printf("=== addon manager start — launcher %s ===", LauncherVersion)

	return &Manager{
		DataDir:    dataDir,
		InstallDir: installDir,
		Logger:     logger,
	}
}

func (m *Manager) log(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if m.Logger != nil {
		m.Logger.Println(msg)
	}
}

// ListInstalled returns all installed addons.
func (m *Manager) ListInstalled() ([]InstalledAddon, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := loadState(m.DataDir)
	if err != nil {
		return nil, err
	}
	return s.Addons, nil
}

// InstallFromRepo downloads and installs an addon from a GitHub repo URL.
func (m *Manager) InstallFromRepo(repoURL string, onProgress func(DownloadProgress)) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	report := func(p DownloadProgress) {
		m.log("[%s] %.0f%% %s", p.Status, p.Percent, p.Message)
		if onProgress != nil {
			onProgress(p)
		}
	}

	// Normalize repo URL
	repoURL = strings.TrimRight(repoURL, "/")
	repoURL = strings.TrimSuffix(repoURL, ".git")

	// Extract owner/repo from URL
	ownerRepo := extractOwnerRepo(repoURL)
	if ownerRepo == "" {
		return fmt.Errorf("invalid GitHub URL: %s", repoURL)
	}

	m.log("InstallFromRepo: %s (owner/repo: %s, installDir: %s)", repoURL, ownerRepo, m.InstallDir)
	report(DownloadProgress{Status: "downloading", Percent: 0, Message: "Downloading addon..."})

	// Download repo as tarball via GitHub API
	tarURL := fmt.Sprintf("https://api.github.com/repos/%s/tarball", ownerRepo)
	m.log("Downloading tarball: %s", tarURL)
	tmpDir, err := os.MkdirTemp(m.DataDir, "addon-download-*")
	if err != nil {
		os.MkdirAll(m.DataDir, 0755)
		tmpDir, err = os.MkdirTemp(m.DataDir, "addon-download-*")
		if err != nil {
			m.log("ERROR: create temp dir: %v", err)
			return fmt.Errorf("create temp dir: %w", err)
		}
	}
	defer os.RemoveAll(tmpDir)

	extractDir := filepath.Join(tmpDir, "extracted")
	if err := downloadAndExtractTarball(tarURL, extractDir, func(pct float64, msg string) {
		report(DownloadProgress{Status: "downloading", Percent: pct, Message: msg})
	}); err != nil {
		m.log("ERROR: download: %v", err)
		return fmt.Errorf("download repo: %w", err)
	}
	m.log("Download and extraction complete")

	// Find the extracted root (GitHub tarballs have a root dir like owner-repo-sha/)
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return fmt.Errorf("read extract dir: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("empty archive — no files extracted")
	}
	// Find the first directory entry (the repo root)
	repoRoot := ""
	for _, e := range entries {
		if e.IsDir() {
			repoRoot = filepath.Join(extractDir, e.Name())
			break
		}
	}
	if repoRoot == "" {
		repoRoot = filepath.Join(extractDir, entries[0].Name())
	}
	m.log("Repo root: %s", repoRoot)

	// Parse addon.json
	report(DownloadProgress{Status: "extracting", Percent: 60, Message: "Reading addon manifest..."})

	manifestPath := filepath.Join(repoRoot, "addon.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("addon.json not found in repo: %w", err)
	}

	var manifest AddonManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("parse addon.json: %w", err)
	}

	m.log("Manifest: id=%s name=%s version=%s files=%d fetch=%d",
		manifest.ID, manifest.Name, manifest.Version, len(manifest.Files), len(manifest.Fetch))

	// Validate manifest schema and load existing state before any disk
	// mutation — fail fast on bad input.
	if err := validateManifest(manifest); err != nil {
		return err
	}
	s, err := loadState(m.DataDir)
	if err != nil {
		return err
	}
	for _, a := range s.Addons {
		if a.ID == manifest.ID {
			return fmt.Errorf("addon '%s' is already installed (version %s)", manifest.Name, a.Version)
		}
	}
	if err := validateRequiresInstalled(manifest, s); err != nil {
		return err
	}
	// Refuse install only when a conflict is *enabled* — disabled conflicts
	// can coexist on disk; the user toggles between them.
	if err := validateConflicts(manifest, s, false); err != nil {
		return err
	}

	report(DownloadProgress{Status: "installing", Percent: 60, Message: "Caching addon files..."})

	cacheDir := addonFilesDir(m.DataDir, manifest.ID)
	os.RemoveAll(cacheDir)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}

	// IMPORTANT — fetch entries run BEFORE repo files[]. Both write to the
	// same cacheDir, so whichever runs LAST wins on path collisions. Putting
	// repo files[] last lets addons override individual files from a
	// fetched archive (e.g. ship a tuned version of one .fx shader on top
	// of the upstream pack). Reverse order would make fetches always win,
	// which is the wrong default for "addon ships X to override fetched X".
	for fIdx, entry := range manifest.Fetch {
		pct := 60.0 + (float64(fIdx)/float64(len(manifest.Fetch)+1))*10.0
		if !platformMatches(entry.OS) {
			m.log("Skipping fetch [%d] %s — not for %s", fIdx+1, entry.From, runtimeGOOS())
			continue
		}
		report(DownloadProgress{Status: "installing", Percent: pct, Message: fmt.Sprintf("Fetching %s...", entry.From)})
		if err := m.stageFetched(entry, cacheDir, func(msg string) {
			report(DownloadProgress{Status: "installing", Percent: pct, Message: msg})
		}); err != nil {
			return fmt.Errorf("fetch %s: %w", entry.From, err)
		}
	}

	// Repo files[] copied AFTER fetches — see note above. They form the
	// authoritative top of the override stack from this addon.
	totalEntries := len(manifest.Files)
	for idx, fe := range manifest.Files {
		pct := 70.0 + (float64(idx)/float64(totalEntries+1))*10.0
		if !platformMatches(fe.OS) {
			m.log("Skipping file [%d/%d] %s — not for %s", idx+1, totalEntries, fe.Src, runtimeGOOS())
			continue
		}
		srcPath := filepath.Join(repoRoot, filepath.FromSlash(fe.Src))
		cacheDst := filepath.Join(cacheDir, filepath.FromSlash(fe.Dst))
		m.log("Caching [%d/%d]: %s -> %s", idx+1, totalEntries, srcPath, cacheDst)

		if _, serr := os.Stat(srcPath); serr != nil {
			m.log("SKIP: source not found: %s (%v)", srcPath, serr)
			report(DownloadProgress{Status: "installing", Percent: pct, Message: fmt.Sprintf("Skipping %s (not found)...", fe.Src)})
			continue
		}
		report(DownloadProgress{Status: "installing", Percent: pct, Message: fmt.Sprintf("Caching %s...", fe.Dst)})
		if err := copyTree(srcPath, cacheDst); err != nil {
			return fmt.Errorf("cache addon files: %w", err)
		}
	}

	// Capture pristine snapshots for any path this addon will touch that
	// hasn't been snapshotted yet. The snapshot is recorded ONCE per path,
	// shared across all addons — that's what makes layered restore correct.
	report(DownloadProgress{Status: "installing", Percent: 80, Message: "Snapshotting originals..."})
	paths, err := m.pathsTouched(manifest.ID)
	if err != nil {
		return fmt.Errorf("walk addon cache: %w", err)
	}
	if err := m.captureSnapshots(s, paths); err != nil {
		return fmt.Errorf("capture pristine: %w", err)
	}

	// Newly installed addons go to the top of the stack: max(existing) + 1.
	// User can reorder later via SetPriority/Reorder.
	priority := highestPriority(s.Addons) + 1

	s.Addons = append(s.Addons, InstalledAddon{
		ID:          manifest.ID,
		RepoURL:     repoURL,
		Version:     manifest.Version,
		Enabled:     true,
		InstalledAt: time.Now(),
		Manifest:    manifest,
		Priority:    priority,
	})

	report(DownloadProgress{Status: "installing", Percent: 90, Message: "Stamping addon files..."})
	if err := m.recomputeStack(s); err != nil {
		return fmt.Errorf("recompute stack: %w", err)
	}

	if err := saveState(m.DataDir, s); err != nil {
		return err
	}

	report(DownloadProgress{Status: "done", Percent: 100, Message: fmt.Sprintf("'%s' v%s installed", manifest.Name, manifest.Version)})
	return nil
}

// Uninstall removes an addon and restores original files.
func (m *Manager) Uninstall(addonID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.uninstallLocked(addonID, false)
}

// uninstallLocked removes an addon. If allowDependents is true, the
// dependent-check is skipped — used by Update for in-place replacement,
// where the addon ID survives so dependents stay valid.
func (m *Manager) uninstallLocked(addonID string, allowDependents bool) error {
	m.log("Uninstall: %s", addonID)

	s, err := loadState(m.DataDir)
	if err != nil {
		return err
	}

	idx := -1
	for i, a := range s.Addons {
		if a.ID == addonID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("addon '%s' not found", addonID)
	}

	// Refuse if any other addon requires this one (unless caller explicitly
	// allows it — Update does, since the addon ID survives).
	if !allowDependents {
		if dependents := dependentsOf(addonID, s.Addons); len(dependents) > 0 {
			return fmt.Errorf("cannot uninstall '%s' — required by: %s", addonID, strings.Join(dependents, ", "))
		}
	}

	// Remove from state first so recomputeStack restores its files from pristine.
	s.Addons = append(s.Addons[:idx], s.Addons[idx+1:]...)

	if err := m.recomputeStack(s); err != nil {
		m.log("Uninstall: recomputeStack failed: %v (continuing)", err)
	}

	// Remove addon cache + legacy backup dir.
	addonPath := addonDir(m.DataDir, addonID)
	m.log("Removing addon data: %s", addonPath)
	os.RemoveAll(addonPath)

	return saveState(m.DataDir, s)
}

// Enable marks an addon enabled and rebuilds the install stack. If the addon
// has unmet `requires`, those are auto-enabled first (in dep order). If any
// `conflicts` addon is enabled, returns an error.
func (m *Manager) Enable(addonID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log("Enable: %s", addonID)

	s, err := loadState(m.DataDir)
	if err != nil {
		return err
	}

	idx := indexByID(s.Addons, addonID)
	if idx < 0 {
		return fmt.Errorf("addon '%s' not found", addonID)
	}
	if s.Addons[idx].Enabled {
		return nil
	}

	// Refuse if conflicting addon is enabled.
	if err := validateConflicts(s.Addons[idx].Manifest, s, false); err != nil {
		return err
	}
	// Cycle check before any mutation.
	if err := detectRequiresCycle(addonID, s.Addons); err != nil {
		return err
	}

	// Auto-enable transitive deps in topological order. validateRequires
	// errors out if any required addon is missing entirely.
	toEnable, err := transitiveRequires(addonID, s.Addons)
	if err != nil {
		return err
	}
	for _, id := range toEnable {
		i := indexByID(s.Addons, id)
		if i < 0 {
			continue
		}
		if !s.Addons[i].Enabled {
			m.log("Enable: auto-enabling dependency %s", id)
		}
		s.Addons[i].Enabled = true
	}

	cacheDir := addonFilesDir(m.DataDir, addonID)
	if _, serr := os.Stat(cacheDir); os.IsNotExist(serr) {
		return fmt.Errorf("addon cache missing — reinstall '%s'", addonID)
	}

	if err := m.recomputeStack(s); err != nil {
		return fmt.Errorf("recompute: %w", err)
	}
	return saveState(m.DataDir, s)
}

// Disable marks an addon disabled and rebuilds the install stack. Refuses if
// any other enabled addon requires this one.
func (m *Manager) Disable(addonID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log("Disable: %s", addonID)

	s, err := loadState(m.DataDir)
	if err != nil {
		return err
	}

	idx := indexByID(s.Addons, addonID)
	if idx < 0 {
		return fmt.Errorf("addon '%s' not found", addonID)
	}
	if !s.Addons[idx].Enabled {
		return nil
	}

	// Refuse if any *enabled* addon requires this one.
	if dependents := enabledDependentsOf(addonID, s.Addons); len(dependents) > 0 {
		return fmt.Errorf("cannot disable '%s' — required by enabled addon(s): %s", addonID, strings.Join(dependents, ", "))
	}

	s.Addons[idx].Enabled = false
	if err := m.recomputeStack(s); err != nil {
		return fmt.Errorf("recompute: %w", err)
	}
	return saveState(m.DataDir, s)
}

// Reorder assigns priorities 0..N-1 to addons in the order given. IDs not in
// orderedIDs keep their existing priority but are bumped above the reordered
// set so the explicit list always wins. Triggers a stack recompute.
func (m *Manager) Reorder(orderedIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := loadState(m.DataDir)
	if err != nil {
		return err
	}

	for newPri, id := range orderedIDs {
		i := indexByID(s.Addons, id)
		if i < 0 {
			return fmt.Errorf("addon '%s' not found", id)
		}
		s.Addons[i].Priority = newPri
	}

	if err := m.recomputeStack(s); err != nil {
		return fmt.Errorf("recompute: %w", err)
	}
	return saveState(m.DataDir, s)
}

// SetPriority sets a single addon's priority and rebuilds the stack.
func (m *Manager) SetPriority(addonID string, priority int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := loadState(m.DataDir)
	if err != nil {
		return err
	}
	i := indexByID(s.Addons, addonID)
	if i < 0 {
		return fmt.Errorf("addon '%s' not found", addonID)
	}
	s.Addons[i].Priority = priority
	if err := m.recomputeStack(s); err != nil {
		return fmt.Errorf("recompute: %w", err)
	}
	return saveState(m.DataDir, s)
}

// Update re-downloads and re-applies an addon, preserving the user's
// priority and enabled state across the in-place replacement.
func (m *Manager) Update(addonID string, onProgress func(DownloadProgress)) error {
	m.mu.Lock()

	m.log("Update: %s", addonID)

	s, err := loadState(m.DataDir)
	if err != nil {
		m.mu.Unlock()
		return err
	}

	idx := indexByID(s.Addons, addonID)
	if idx < 0 {
		m.mu.Unlock()
		return fmt.Errorf("addon '%s' not found", addonID)
	}
	prevRepoURL := s.Addons[idx].RepoURL
	prevPriority := s.Addons[idx].Priority
	prevEnabled := s.Addons[idx].Enabled

	// Uninstall old version (already holding lock). Allow dependents — the
	// addon ID stays the same after re-install so dep graph is preserved.
	if err := m.uninstallLocked(addonID, true); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("remove old version: %w", err)
	}

	// Release lock before InstallFromRepo (which takes its own lock).
	m.mu.Unlock()

	if err := m.InstallFromRepo(prevRepoURL, onProgress); err != nil {
		return err
	}

	// Re-apply the user's priority and enabled state.
	m.mu.Lock()
	defer m.mu.Unlock()
	s2, err := loadState(m.DataDir)
	if err != nil {
		return err
	}
	i := indexByID(s2.Addons, addonID)
	if i < 0 {
		return nil // install succeeded but somehow the addon isn't in state
	}
	s2.Addons[i].Priority = prevPriority
	s2.Addons[i].Enabled = prevEnabled
	if err := m.recomputeStack(s2); err != nil {
		return fmt.Errorf("recompute after update: %w", err)
	}
	return saveState(m.DataDir, s2)
}

// CheckUpdates queries GitHub for newer versions of installed addons.
func (m *Manager) CheckUpdates() ([]AddonUpdate, error) {
	s, err := loadState(m.DataDir)
	if err != nil {
		return nil, err
	}

	var updates []AddonUpdate
	for _, a := range s.Addons {
		ownerRepo := extractOwnerRepo(a.RepoURL)
		if ownerRepo == "" {
			continue
		}

		latest, err := fetchLatestTag(ownerRepo)
		if err != nil || latest == "" {
			continue
		}

		if latest != a.Version {
			updates = append(updates, AddonUpdate{
				AddonID:        a.ID,
				CurrentVersion: a.Version,
				LatestVersion:  latest,
				RepoURL:        a.RepoURL,
			})
		}
	}
	return updates, nil
}

// MissingExpected walks each installed addon's manifest.Expects list and
// returns a map of addon ID -> still-missing install-dir-relative paths.
// Recomputed live on every call (no persistence) so the UI reflects the user
// completing manual install steps without needing a re-install.
func (m *Manager) MissingExpected() map[string][]string {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := loadState(m.DataDir)
	if err != nil {
		return nil
	}
	out := make(map[string][]string)
	for _, a := range s.Addons {
		var missing []string
		for _, rel := range a.Manifest.Expects {
			rel = strings.TrimSpace(rel)
			if rel == "" {
				continue
			}
			path := filepath.Join(m.InstallDir, filepath.FromSlash(rel))
			if _, serr := os.Stat(path); serr != nil {
				missing = append(missing, rel)
			}
		}
		if len(missing) > 0 {
			out[a.ID] = missing
		}
	}
	return out
}

// EnabledDLLOverrides returns the union of WineDLLOverrides declared by every
// enabled addon, walked in priority order so the resulting slice is stable
// across runs. Basenames are lowercased and de-duplicated. Suitable for
// composing into WINEDLLOVERRIDES at game-launch time.
func (m *Manager) EnabledDLLOverrides() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := loadState(m.DataDir)
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var out []string
	for _, a := range enabledInOrder(s.Addons) {
		for _, dll := range a.Manifest.WineDLLOverrides {
			key := strings.ToLower(strings.TrimSpace(dll))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// ReapplyEnabled rebuilds the install dir from pristine + the current enabled
// stack. Call after the CDN updater finishes — but PrepareForUpdate +
// FinishAfterUpdate is the preferred two-phase flow because it correctly
// refreshes the pristine pool when the CDN updates pristine paths.
func (m *Manager) ReapplyEnabled() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := loadState(m.DataDir)
	if err != nil {
		return err
	}
	if err := m.recomputeStack(s); err != nil {
		return err
	}
	return saveState(m.DataDir, s)
}

// PrepareForUpdate restores all addon-touched paths to their pristine state
// so the CDN updater can safely overwrite them. Does NOT mutate the addon
// state — enabled flags are preserved for FinishAfterUpdate to re-stamp.
func (m *Manager) PrepareForUpdate() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := loadState(m.DataDir)
	if err != nil {
		return err
	}
	for rel := range s.PristineSnapshots {
		if err := m.restorePristine(rel, s); err != nil {
			m.log("PrepareForUpdate: restore %s failed: %v", rel, err)
		}
	}
	return nil
}

// FinishAfterUpdate refreshes the pristine pool from the current install dir
// (which now reflects the CDN-updated pristine), then re-stamps the enabled
// addon stack. Call after a successful CDN update.
func (m *Manager) FinishAfterUpdate() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := loadState(m.DataDir)
	if err != nil {
		return err
	}
	for rel := range s.PristineSnapshots {
		if err := m.refreshPristineFor(rel, s); err != nil {
			m.log("FinishAfterUpdate: refresh %s failed: %v", rel, err)
		}
	}
	if err := m.recomputeStack(s); err != nil {
		return err
	}
	return saveState(m.DataDir, s)
}

// --- Internal helpers ---

// pathsTouched walks an addon's cache dir and returns install-relative paths.
func (m *Manager) pathsTouched(addonID string) ([]string, error) {
	cacheDir := addonFilesDir(m.DataDir, addonID)
	var paths []string
	err := filepath.Walk(cacheDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() || info.Name() == ".gitkeep" {
			return nil
		}
		rel, rerr := filepath.Rel(cacheDir, p)
		if rerr != nil {
			return rerr
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return paths, err
}

// captureSnapshots records pristine state for any path the addon will touch
// that hasn't been snapshotted yet. The first capture wins — that's what
// makes this safe to call multiple times across reinstalls.
func (m *Manager) captureSnapshots(s *state, paths []string) error {
	if s.PristineSnapshots == nil {
		s.PristineSnapshots = make(map[string]string)
	}
	pristineDir := pristinePoolDir(m.DataDir)
	for _, rel := range paths {
		if _, ok := s.PristineSnapshots[rel]; ok {
			continue
		}
		gamePath := filepath.Join(m.InstallDir, filepath.FromSlash(rel))
		if _, err := os.Stat(gamePath); err == nil {
			dst := filepath.Join(pristineDir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return err
			}
			if err := copyFile(gamePath, dst); err != nil {
				return fmt.Errorf("snapshot %s: %w", rel, err)
			}
			s.PristineSnapshots[rel] = PristineExists
		} else {
			s.PristineSnapshots[rel] = PristineAbsent
		}
	}
	return nil
}

// refreshPristineFor updates one snapshot from the current install-dir state.
// Used post-CDN-update once the install dir reflects fresh pristine.
func (m *Manager) refreshPristineFor(rel string, s *state) error {
	if s.PristineSnapshots == nil {
		s.PristineSnapshots = make(map[string]string)
	}
	gamePath := filepath.Join(m.InstallDir, filepath.FromSlash(rel))
	pristinePath := filepath.Join(pristinePoolDir(m.DataDir), filepath.FromSlash(rel))
	if _, err := os.Stat(gamePath); err == nil {
		if err := os.MkdirAll(filepath.Dir(pristinePath), 0755); err != nil {
			return err
		}
		if err := copyFile(gamePath, pristinePath); err != nil {
			return err
		}
		s.PristineSnapshots[rel] = PristineExists
	} else {
		os.Remove(pristinePath)
		s.PristineSnapshots[rel] = PristineAbsent
	}
	return nil
}

// restorePristine restores a path to its pre-any-addon state.
func (m *Manager) restorePristine(rel string, s *state) error {
	gamePath := filepath.Join(m.InstallDir, filepath.FromSlash(rel))
	snapshot, ok := s.PristineSnapshots[rel]
	if !ok {
		return nil // never tracked — leave alone
	}
	switch snapshot {
	case PristineExists:
		src := filepath.Join(pristinePoolDir(m.DataDir), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(gamePath), 0755); err != nil {
			return err
		}
		return copyFile(src, gamePath)
	case PristineAbsent:
		if err := os.Remove(gamePath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// recomputeStack rebuilds the install dir from pristine + enabled addons in
// priority order. Idempotent — safe to call after any state change.
//
// The restore phase iterates s.PristineSnapshots (the authoritative record of
// every path any addon has ever touched), NOT the current addons' cache dirs.
// That distinction matters on Uninstall: the addon is removed from s.Addons
// before recompute runs, but its old paths are still in PristineSnapshots and
// must be restored.
func (m *Manager) recomputeStack(s *state) error {
	for rel := range s.PristineSnapshots {
		if err := m.restorePristine(rel, s); err != nil {
			m.log("recomputeStack: restore %s failed: %v", rel, err)
		}
	}
	for _, a := range enabledInOrder(s.Addons) {
		cacheDir := addonFilesDir(m.DataDir, a.ID)
		if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
			m.log("recomputeStack: cache missing for %s — skipping", a.ID)
			continue
		}
		if err := m.stampAddon(a.ID, cacheDir); err != nil {
			m.log("recomputeStack: stamp %s failed: %v", a.ID, err)
		}
	}
	return nil
}

// stampAddon copies the addon's cache files into the install dir. No backup
// logic — pristine pool handles restore.
func (m *Manager) stampAddon(addonID, cacheDir string) error {
	count := 0
	err := filepath.Walk(cacheDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if info.Name() == ".gitkeep" {
			return nil
		}
		rel, rerr := filepath.Rel(cacheDir, p)
		if rerr != nil {
			return rerr
		}
		dst := filepath.Join(m.InstallDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		if err := copyFile(p, dst); err != nil {
			return err
		}
		count++
		return nil
	})
	m.log("stampAddon: %s — %d files", addonID, count)
	return err
}

// enabledInOrder returns enabled addons sorted by Priority asc, ties broken
// by InstalledAt asc. Lower priority = applied first; higher = wins.
func enabledInOrder(addons []InstalledAddon) []InstalledAddon {
	enabled := make([]InstalledAddon, 0, len(addons))
	for _, a := range addons {
		if a.Enabled {
			enabled = append(enabled, a)
		}
	}
	sort.SliceStable(enabled, func(i, j int) bool {
		if enabled[i].Priority != enabled[j].Priority {
			return enabled[i].Priority < enabled[j].Priority
		}
		return enabled[i].InstalledAt.Before(enabled[j].InstalledAt)
	})
	return enabled
}

func indexByID(addons []InstalledAddon, id string) int {
	for i, a := range addons {
		if a.ID == id {
			return i
		}
	}
	return -1
}

func highestPriority(addons []InstalledAddon) int {
	max := -1
	for _, a := range addons {
		if a.Priority > max {
			max = a.Priority
		}
	}
	return max
}

// --- Dependency / conflict resolver ---

// validateManifest catches authoring mistakes early — before any disk
// mutation or network round-trip. Returns the first problem it finds.
func validateManifest(m AddonManifest) error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("addon.json: 'id' is required")
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("addon.json: 'name' is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("addon.json: 'version' is required")
	}
	if len(m.Files) == 0 && len(m.WineDLLOverrides) == 0 && len(m.Fetch) == 0 && len(m.EnvVars) == 0 {
		return fmt.Errorf("addon.json: must declare at least one entry in 'files', 'fetch', 'wineDllOverrides', or 'envVars'")
	}
	for _, req := range m.Requires {
		if req == m.ID {
			return fmt.Errorf("addon.json: '%s' lists itself in 'requires'", m.ID)
		}
	}
	for _, c := range m.Conflicts {
		if c == m.ID {
			return fmt.Errorf("addon.json: '%s' lists itself in 'conflicts'", m.ID)
		}
	}
	if dup := firstDuplicate(m.Requires); dup != "" {
		return fmt.Errorf("addon.json: duplicate entry in 'requires': %s", dup)
	}
	if dup := firstDuplicate(m.Conflicts); dup != "" {
		return fmt.Errorf("addon.json: duplicate entry in 'conflicts': %s", dup)
	}
	for _, f := range m.Files {
		if strings.TrimSpace(f.Src) == "" || strings.TrimSpace(f.Dst) == "" {
			return fmt.Errorf("addon.json: 'files' entries need both 'src' and 'dst'")
		}
		if strings.Contains(f.Dst, "..") {
			return fmt.Errorf("addon.json: 'dst' must not contain '..' (got %q)", f.Dst)
		}
	}
	for _, exp := range m.Expects {
		if strings.TrimSpace(exp) == "" {
			return fmt.Errorf("addon.json: 'expects' entries must not be empty strings")
		}
		if strings.Contains(exp, "..") {
			return fmt.Errorf("addon.json: 'expects' must not contain '..' (got %q)", exp)
		}
	}
	for _, fe := range m.Fetch {
		if strings.TrimSpace(fe.From) == "" {
			return fmt.Errorf("addon.json: 'fetch' entries need 'from' URL")
		}
		if !strings.HasPrefix(fe.From, "http://") && !strings.HasPrefix(fe.From, "https://") {
			return fmt.Errorf("addon.json: 'fetch.from' must be http(s) URL (got %q)", fe.From)
		}
		switch fe.Extract {
		case "", "zip", "tar.gz", "tgz", "exe":
			// supported
		default:
			return fmt.Errorf("addon.json: 'fetch.extract' must be 'zip', 'tar.gz', 'exe', or empty (got %q)", fe.Extract)
		}
		if len(fe.Files) == 0 {
			return fmt.Errorf("addon.json: 'fetch' entries need at least one 'files' mapping")
		}
		for _, f := range fe.Files {
			if strings.TrimSpace(f.Dst) == "" {
				return fmt.Errorf("addon.json: 'fetch.files' entries need 'dst'")
			}
			if strings.Contains(f.Dst, "..") {
				return fmt.Errorf("addon.json: 'fetch.files.dst' must not contain '..' (got %q)", f.Dst)
			}
		}
	}
	for platform, vars := range m.EnvVars {
		if !validOS(platform) {
			return fmt.Errorf("addon.json: 'envVars' platform key %q must be one of linux, darwin, windows", platform)
		}
		for k := range vars {
			if strings.TrimSpace(k) == "" {
				return fmt.Errorf("addon.json: 'envVars[%s]' has empty variable name", platform)
			}
		}
	}
	return nil
}

// validOS rejects platform keys we don't know how to handle. We list the
// three runtime.GOOS values the launcher currently builds for; freebsd /
// openbsd / etc. are unsupported intentionally.
func validOS(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "linux", "darwin", "windows":
		return true
	}
	return false
}

func firstDuplicate(xs []string) string {
	seen := map[string]bool{}
	for _, x := range xs {
		if seen[x] {
			return x
		}
		seen[x] = true
	}
	return ""
}

// validateRequiresInstalled errors if any of manifest.Requires is not present
// in the installed set. Used at install time.
func validateRequiresInstalled(manifest AddonManifest, s *state) error {
	var missing []string
	for _, req := range manifest.Requires {
		if indexByID(s.Addons, req) < 0 {
			missing = append(missing, req)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("addon '%s' requires not-installed addon(s): %s", manifest.ID, strings.Join(missing, ", "))
	}
	return nil
}

// validateConflicts errors if any addon in manifest.Conflicts is enabled
// (or installed, when checkInstalled=true).
func validateConflicts(manifest AddonManifest, s *state, checkInstalled bool) error {
	var bad []string
	for _, c := range manifest.Conflicts {
		i := indexByID(s.Addons, c)
		if i < 0 {
			continue
		}
		if checkInstalled || s.Addons[i].Enabled {
			bad = append(bad, c)
		}
	}
	if len(bad) > 0 {
		verb := "enabled"
		if checkInstalled {
			verb = "installed"
		}
		return fmt.Errorf("addon '%s' conflicts with %s addon(s): %s", manifest.ID, verb, strings.Join(bad, ", "))
	}
	// Reverse check: any other enabled addon that lists THIS one as a conflict.
	for _, other := range s.Addons {
		if other.ID == manifest.ID {
			continue
		}
		if checkInstalled || other.Enabled {
			for _, c := range other.Manifest.Conflicts {
				if c == manifest.ID {
					return fmt.Errorf("addon '%s' is listed as a conflict by %s addon '%s'", manifest.ID, map[bool]string{true: "installed", false: "enabled"}[checkInstalled], other.ID)
				}
			}
		}
	}
	return nil
}

// detectRequiresCycle returns an error if the requires graph reachable from
// rootID contains a cycle.
func detectRequiresCycle(rootID string, addons []InstalledAddon) error {
	visited := make(map[string]bool)
	stack := make(map[string]bool)
	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		if stack[id] {
			return fmt.Errorf("requires cycle: %s -> %s", strings.Join(path, " -> "), id)
		}
		if visited[id] {
			return nil
		}
		visited[id] = true
		stack[id] = true
		i := indexByID(addons, id)
		if i >= 0 {
			for _, req := range addons[i].Manifest.Requires {
				if err := visit(req, append(path, id)); err != nil {
					return err
				}
			}
		}
		stack[id] = false
		return nil
	}
	return visit(rootID, nil)
}

// transitiveRequires returns the IDs to enable in topological order (deps
// first, root last) so the caller can flip Enabled flags safely. Errors if
// any required addon is not installed.
func transitiveRequires(rootID string, addons []InstalledAddon) ([]string, error) {
	visited := make(map[string]bool)
	var order []string
	var visit func(id string) error
	visit = func(id string) error {
		if visited[id] {
			return nil
		}
		visited[id] = true
		i := indexByID(addons, id)
		if i < 0 {
			return fmt.Errorf("required addon '%s' is not installed", id)
		}
		for _, req := range addons[i].Manifest.Requires {
			if err := visit(req); err != nil {
				return err
			}
		}
		order = append(order, id)
		return nil
	}
	if err := visit(rootID); err != nil {
		return nil, err
	}
	return order, nil
}

// dependentsOf returns IDs of installed addons whose manifests list addonID
// in their requires.
func dependentsOf(addonID string, addons []InstalledAddon) []string {
	var out []string
	for _, a := range addons {
		for _, req := range a.Manifest.Requires {
			if req == addonID {
				out = append(out, a.ID)
				break
			}
		}
	}
	return out
}

// enabledDependentsOf is dependentsOf restricted to currently-enabled addons.
func enabledDependentsOf(addonID string, addons []InstalledAddon) []string {
	var out []string
	for _, a := range addons {
		if !a.Enabled {
			continue
		}
		for _, req := range a.Manifest.Requires {
			if req == addonID {
				out = append(out, a.ID)
				break
			}
		}
	}
	return out
}

func extractOwnerRepo(repoURL string) string {
	repoURL = strings.TrimRight(repoURL, "/")
	repoURL = strings.TrimSuffix(repoURL, ".git")

	// Handle https://github.com/owner/repo or git@github.com:owner/repo
	if strings.Contains(repoURL, "github.com/") {
		parts := strings.SplitN(repoURL, "github.com/", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}
	if strings.Contains(repoURL, "github.com:") {
		parts := strings.SplitN(repoURL, "github.com:", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}
	return ""
}

func fetchLatestTag(ownerRepo string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", ownerRepo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	json.NewDecoder(resp.Body).Decode(&release)
	return release.TagName, nil
}

func downloadAndExtractTarball(url, destDir string, onProgress func(float64, string)) error {
	if onProgress != nil {
		onProgress(5, "Downloading archive...")
	}

	// Use plain http.Get which follows redirects automatically.
	// The GitHub API tarball endpoint (302) redirects to a CDN URL.
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, resp.Request.URL.Host, string(body))
	}

	if onProgress != nil {
		onProgress(10, fmt.Sprintf("Downloading (%s)...", resp.Request.URL.Host))
	}

	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("not a gzip archive (Content-Type: %s): %w", resp.Header.Get("Content-Type"), err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	if onProgress != nil {
		onProgress(30, "Extracting files...")
	}

	fileCount := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}

		target := filepath.Join(destDir, header.Name)

		// Prevent path traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			f, err := os.Create(target)
			if err != nil {
				return err
			}
			io.Copy(f, tr)
			f.Close()
			fileCount++
			if onProgress != nil && fileCount%100 == 0 {
				pct := 30.0 + float64(fileCount)/100.0 // rough progress
				if pct > 54 {
					pct = 54
				}
				onProgress(pct, fmt.Sprintf("Extracting... %d files", fileCount))
			}
		}
	}

	if onProgress != nil {
		onProgress(55, fmt.Sprintf("Extracted %d files", fileCount))
	}
	return nil
}

// platformMatches reports whether the current OS is allowed by the given
// list. Empty list means "applies on every platform" (the default for
// addons authored before the os field existed).
func platformMatches(allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	cur := runtimeGOOS()
	for _, os := range allowed {
		if strings.EqualFold(strings.TrimSpace(os), cur) {
			return true
		}
	}
	return false
}

// runtimeGOOS is a thin indirection so tests can stub it. We don't bother
// with sync.atomic because the only writer is test setup.
var goosOverride string

func runtimeGOOS() string {
	if goosOverride != "" {
		return goosOverride
	}
	return runtime.GOOS
}

// EnabledEnvVars returns the merged set of environment variables that
// enabled addons want set on the game process for the current platform,
// after expanding the "${INSTALL_DIR}" template against m.InstallDir.
//
// When two enabled addons declare the same key, the higher-priority addon
// wins (matching how files stack). Same priority — order of installation
// breaks the tie via enabledInOrder().
func (m *Manager) EnabledEnvVars() (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := loadState(m.DataDir)
	if err != nil {
		return nil, err
	}
	cur := runtimeGOOS()
	out := map[string]string{}
	for _, a := range enabledInOrder(s.Addons) {
		platVars, ok := a.Manifest.EnvVars[cur]
		if !ok {
			continue
		}
		for k, v := range platVars {
			out[k] = strings.ReplaceAll(v, "${INSTALL_DIR}", m.InstallDir)
		}
	}
	return out, nil
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(src, path)
			return copyFile(path, filepath.Join(dst, rel))
		})
	}

	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	os.MkdirAll(filepath.Dir(dst), 0755)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

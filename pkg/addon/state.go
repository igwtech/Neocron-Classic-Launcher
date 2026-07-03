package addon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// AddonManifest is the addon.json parsed from a GitHub repo.
type AddonManifest struct {
	Name        string      `json:"name"`
	ID          string      `json:"id"`
	Version     string      `json:"version"`
	Author      string      `json:"author"`
	Description string      `json:"description"`
	Category    string      `json:"category"` // graphics, audio, ui, scripts, translation, effects, other
	Tags        []string    `json:"tags"`
	Files       []FileEntry `json:"files"`
	Conflicts   []string    `json:"conflicts"`
	Requires    []string    `json:"requires"`
	// WineDLLOverrides lists DLL basenames (no extension) that should be set to
	// "native,builtin" in WINEDLLOVERRIDES when this addon is enabled. Used by
	// wrapper addons like dgVoodoo2 (d3d8, d3dimm, ddraw) and ReShade (dxgi, d3d9).
	WineDLLOverrides []string `json:"wineDllOverrides,omitempty"`
	// Fetch declares external archives the launcher should download at install
	// time and extract into the addon cache. Lets addons reference upstream
	// binaries (dgVoodoo2 from GitHub releases, etc.) without redistributing
	// them in the addon repo itself.
	Fetch []FetchEntry `json:"fetch,omitempty"`
	// Expects lists install-dir-relative paths that the addon needs the user
	// to provide manually (e.g. ReShade's dxgi.dll, which upstream asks not
	// to be redistributed). The launcher checks these after install + on
	// every refresh, surfacing missing paths as a warning badge.
	Expects []string `json:"expects,omitempty"`
	// EnvVars sets extra environment variables on the game process, keyed by
	// runtime.GOOS. Lets addons activate Vulkan layers (vkBasalt), tweak
	// engine envs, etc. without launcher code changes. Values may include
	// the literal "${INSTALL_DIR}" which is expanded to the game install
	// directory at launch time. Outer key e.g. "linux", "darwin", "windows".
	EnvVars map[string]map[string]string `json:"envVars,omitempty"`
}

// FileEntry maps source paths in the addon repo to destination paths in the game dir.
// OS, when non-empty, restricts staging to those runtime.GOOS values; an empty
// list means the entry applies on every platform (current default behavior).
type FileEntry struct {
	Src string   `json:"src"`
	Dst string   `json:"dst"`
	OS  []string `json:"os,omitempty"`
}

// FetchEntry declares an external download. The launcher fetches `From` at
// install time, extracts per `Extract`, and copies the listed `Files` into
// the addon cache alongside files mapped from the repo itself.
// OS restricts the fetch to the listed platforms (empty = all).
type FetchEntry struct {
	From    string      `json:"from"`              // URL — http(s) only, follows redirects
	Extract string      `json:"extract,omitempty"` // "zip", "tar.gz", or "" for raw single-file
	Files   []FileEntry `json:"files"`             // src is path inside extracted archive (or "" for raw)
	OS      []string    `json:"os,omitempty"`
}

// InstalledAddon tracks an addon that has been installed.
type InstalledAddon struct {
	ID          string        `json:"id"`
	RepoURL     string        `json:"repoUrl"`
	Version     string        `json:"version"`
	Enabled     bool          `json:"enabled"`
	InstalledAt time.Time     `json:"installedAt"`
	Manifest    AddonManifest `json:"manifest"`
	// Priority controls apply order: lower priorities apply first, higher
	// priorities overwrite when two addons declare the same destination path.
	// Default 0; ties broken by InstalledAt ascending.
	Priority int `json:"priority"`
}

// AddonUpdate describes an available update for an installed addon.
type AddonUpdate struct {
	AddonID        string `json:"addonId"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	RepoURL        string `json:"repoUrl"`
}

// DownloadProgress reports addon download/install progress.
type DownloadProgress struct {
	Status  string  `json:"status"` // "downloading", "extracting", "installing", "done", "error"
	Percent float64 `json:"percent"`
	Message string  `json:"message"`
}

// Pristine snapshot states: a path is "exists" if it had a game file before
// any addon touched it, "absent" if no game file existed.
const (
	PristineExists = "exists"
	PristineAbsent = "absent"
)

// state is the persisted addon state.
type state struct {
	Addons []InstalledAddon `json:"addons"`
	// PristineSnapshots records, for each install-relative path that any addon
	// has ever declared, whether the original (pre-addon) game file existed.
	// "exists" means a copy lives in pristinePoolDir; "absent" means restoring
	// pristine = delete the file. Recorded once at first-install, never
	// overwritten — that's what makes layered restore correct.
	PristineSnapshots map[string]string `json:"pristineSnapshots,omitempty"`
}

// DefaultDataDir returns the base directory for addon data.
func DefaultDataDir() string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "neocron-classic-launcher", "addons")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "neocron-classic-launcher", "addons")
}

func statePath(dataDir string) string {
	return filepath.Join(dataDir, "state.json")
}

func loadState(dataDir string) (*state, error) {
	data, err := os.ReadFile(statePath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return &state{}, nil
		}
		return nil, err
	}
	var s state
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveState(dataDir string, s *state) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(dataDir), data, 0644)
}

// addonDir returns the directory for a specific addon's cached files and backups.
func addonDir(dataDir, addonID string) string {
	return filepath.Join(dataDir, addonID)
}

func addonFilesDir(dataDir, addonID string) string {
	return filepath.Join(addonDir(dataDir, addonID), "files")
}

// pristinePoolDir is the shared per-launcher directory holding pristine
// (pre-any-addon) snapshots of game files. Layered backup correctness depends
// on this being shared across addons rather than per-addon.
func pristinePoolDir(dataDir string) string {
	return filepath.Join(dataDir, "_pristine")
}

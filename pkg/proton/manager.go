package proton

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Build represents a discovered or downloaded Proton build.
type Build struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Source  string `json:"source"` // "steam", "ge-proton", "custom"
	Version string `json:"version"`
	Valid   bool   `json:"valid"`
}

// DownloadProgress reports progress during a Proton download.
type DownloadProgress struct {
	Status     string  `json:"status"` // "downloading", "extracting", "done", "error"
	Percent    float64 `json:"percent"`
	BytesTotal int64   `json:"bytesTotal"`
	BytesDone  int64   `json:"bytesDone"`
	Message    string  `json:"message"`
}

// GHRelease is a minimal GitHub release for GE-Proton.
type GHRelease struct {
	TagName string    `json:"tag_name"`
	Name    string    `json:"name"`
	Assets  []GHAsset `json:"assets"`
}

// GHAsset is a GitHub release asset.
type GHAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// Manager handles discovery and management of Proton installations.
type Manager struct {
	DataDir string // ~/.local/share/neocron-launcher/proton/

	mu       sync.Mutex
	progress DownloadProgress
	cancel   chan struct{}
}

// NewManager creates a new Proton manager.
func NewManager() *Manager {
	dataDir := defaultDataDir()
	return &Manager{
		DataDir: dataDir,
		cancel:  make(chan struct{}),
	}
}

func defaultDataDir() string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "neocron-launcher", "proton")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "neocron-launcher", "proton")
}

// GetProgress returns the current download/extract progress.
func (m *Manager) GetProgress() DownloadProgress {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progress
}

func (m *Manager) setProgress(p DownloadProgress) {
	m.mu.Lock()
	m.progress = p
	m.mu.Unlock()
}

// Cancel stops the current download operation.
func (m *Manager) Cancel() {
	select {
	case <-m.cancel:
	default:
		close(m.cancel)
	}
}

// DetectBuilds scans the system for available Proton installations.
func (m *Manager) DetectBuilds() []Build {
	var builds []Build

	// Check Steam's Proton installations
	builds = append(builds, m.detectSteamBuilds()...)

	// Check our own managed Proton builds
	builds = append(builds, m.detectManagedBuilds()...)

	// Validate all builds
	for i := range builds {
		builds[i].Valid = m.validateBuild(builds[i].Path)
	}

	sort.Slice(builds, func(i, j int) bool {
		return builds[i].Name > builds[j].Name // newest first
	})

	return builds
}

func (m *Manager) detectSteamBuilds() []Build {
	var builds []Build
	home, _ := os.UserHomeDir()

	steamPaths := []string{
		filepath.Join(home, ".steam", "root", "compatibilitytools.d"),
		filepath.Join(home, ".steam", "steam", "compatibilitytools.d"),
		filepath.Join(home, ".local", "share", "Steam", "compatibilitytools.d"),
	}

	// Also check steamapps/common for official Proton
	steamAppPaths := []string{
		filepath.Join(home, ".steam", "root", "steamapps", "common"),
		filepath.Join(home, ".steam", "steam", "steamapps", "common"),
		filepath.Join(home, ".local", "share", "Steam", "steamapps", "common"),
	}

	seen := make(map[string]bool)

	for _, base := range steamPaths {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			fullPath := filepath.Join(base, e.Name())
			realPath, _ := filepath.EvalSymlinks(fullPath)
			if seen[realPath] {
				continue
			}
			seen[realPath] = true

			if m.validateBuild(fullPath) {
				builds = append(builds, Build{
					Name:    e.Name(),
					Path:    fullPath,
					Source:  "steam",
					Version: e.Name(),
				})
			}
		}
	}

	for _, base := range steamAppPaths {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "Proton") {
				continue
			}
			fullPath := filepath.Join(base, e.Name())
			realPath, _ := filepath.EvalSymlinks(fullPath)
			if seen[realPath] {
				continue
			}
			seen[realPath] = true

			if m.validateBuild(fullPath) {
				builds = append(builds, Build{
					Name:    e.Name(),
					Path:    fullPath,
					Source:  "steam",
					Version: e.Name(),
				})
			}
		}
	}

	return builds
}

func (m *Manager) detectManagedBuilds() []Build {
	var builds []Build
	entries, err := os.ReadDir(m.DataDir)
	if err != nil {
		return builds
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		fullPath := filepath.Join(m.DataDir, e.Name())
		builds = append(builds, Build{
			Name:    e.Name(),
			Path:    fullPath,
			Source:  "ge-proton",
			Version: e.Name(),
		})
	}
	return builds
}

// validateBuild checks if a path contains a usable Proton installation.
func (m *Manager) validateBuild(path string) bool {
	// Check for the proton launch script
	protonScript := filepath.Join(path, "proton")
	if _, err := os.Stat(protonScript); err == nil {
		return true
	}

	// Some builds use dist/bin/wine
	wineBin := filepath.Join(path, "dist", "bin", "wine")
	if _, err := os.Stat(wineBin); err == nil {
		return true
	}

	// GE-Proton uses files/bin/wine
	wineBin2 := filepath.Join(path, "files", "bin", "wine")
	if _, err := os.Stat(wineBin2); err == nil {
		return true
	}

	return false
}

// FetchAvailableVersions queries GitHub for available GE-Proton releases.
func (m *Manager) FetchAvailableVersions() ([]GHRelease, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/GloriousEggroll/proton-ge-custom/releases?per_page=10", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch GE-Proton releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var releases []GHRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, err
	}

	// Filter to only releases with .tar.gz assets
	var filtered []GHRelease
	for _, r := range releases {
		for _, a := range r.Assets {
			if strings.HasSuffix(a.Name, ".tar.gz") {
				filtered = append(filtered, r)
				break
			}
		}
	}
	return filtered, nil
}

// DownloadBuild downloads and extracts a GE-Proton release.
func (m *Manager) DownloadBuild(release GHRelease, onProgress func(DownloadProgress)) error {
	m.cancel = make(chan struct{})

	report := func(p DownloadProgress) {
		m.setProgress(p)
		if onProgress != nil {
			onProgress(p)
		}
	}

	// Find the .tar.gz asset
	var asset GHAsset
	for _, a := range release.Assets {
		if strings.HasSuffix(a.Name, ".tar.gz") {
			asset = a
			break
		}
	}
	if asset.BrowserDownloadURL == "" {
		return fmt.Errorf("no .tar.gz asset found for %s", release.TagName)
	}

	report(DownloadProgress{
		Status:  "downloading",
		Message: fmt.Sprintf("Downloading %s...", asset.Name),
	})

	// Download to a temp file
	if err := os.MkdirAll(m.DataDir, 0755); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(m.DataDir, "proton-download-*.tar.gz")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	resp, err := http.Get(asset.BrowserDownloadURL)
	if err != nil {
		tmpFile.Close()
		return err
	}
	defer resp.Body.Close()

	totalSize := resp.ContentLength
	if totalSize <= 0 {
		totalSize = asset.Size
	}

	var written int64
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-m.cancel:
			tmpFile.Close()
			return fmt.Errorf("cancelled")
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := tmpFile.Write(buf[:n]); err != nil {
				tmpFile.Close()
				return err
			}
			written += int64(n)
			pct := float64(0)
			if totalSize > 0 {
				pct = float64(written) / float64(totalSize) * 100
			}
			report(DownloadProgress{
				Status:     "downloading",
				Percent:    pct,
				BytesTotal: totalSize,
				BytesDone:  written,
				Message:    fmt.Sprintf("Downloading %s... %.1f%%", asset.Name, pct),
			})
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			tmpFile.Close()
			return readErr
		}
	}
	tmpFile.Close()

	// Extract
	report(DownloadProgress{
		Status:  "extracting",
		Percent: 0,
		Message: fmt.Sprintf("Extracting %s...", release.TagName),
	})

	if err := m.extractTarGz(tmpPath, m.DataDir, func(pct float64) {
		report(DownloadProgress{
			Status:  "extracting",
			Percent: pct,
			Message: fmt.Sprintf("Extracting %s... %.0f%%", release.TagName, pct),
		})
	}); err != nil {
		return err
	}

	report(DownloadProgress{
		Status:  "done",
		Percent: 100,
		Message: fmt.Sprintf("%s installed successfully", release.TagName),
	})
	return nil
}

func (m *Manager) extractTarGz(archivePath, destDir string, onProgress func(float64)) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	info, _ := f.Stat()
	totalSize := info.Size()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var processed int64

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
			if err := os.MkdirAll(target, os.FileMode(header.Mode)|0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}

		processed += header.Size
		if totalSize > 0 && onProgress != nil {
			// Estimate: compressed progress based on raw sizes vs archive size
			pct := float64(processed) / float64(totalSize*3) * 100 // rough ratio
			if pct > 99 {
				pct = 99
			}
			onProgress(pct)
		}
	}

	if onProgress != nil {
		onProgress(100)
	}
	return nil
}

// RemoveBuild deletes a managed Proton build.
func (m *Manager) RemoveBuild(buildPath string) error {
	// Only allow removing builds under our managed directory
	if !strings.HasPrefix(filepath.Clean(buildPath), filepath.Clean(m.DataDir)) {
		return fmt.Errorf("cannot remove builds outside managed directory")
	}
	return os.RemoveAll(buildPath)
}

// GetBuildWineBinary returns the path to the wine binary within a Proton build.
func GetBuildWineBinary(buildPath string) string {
	candidates := []string{
		filepath.Join(buildPath, "dist", "bin", "wine"),
		filepath.Join(buildPath, "files", "bin", "wine"),
		filepath.Join(buildPath, "bin", "wine"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// GetProtonScript returns the path to the proton launch script.
func GetProtonScript(buildPath string) string {
	script := filepath.Join(buildPath, "proton")
	if _, err := os.Stat(script); err == nil {
		return script
	}
	return ""
}

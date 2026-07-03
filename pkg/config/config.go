// Package config holds the NC1 (Neocron Classic) launcher configuration.
//
// Modeled on the official launcher's on-disk state: a single config.json under
// the user config dir, plus a persisted Discord session handled separately by
// pkg/auth. Endpoints mirror the official service topology discovered by RE
// (see docs/RE_LAUNCHER.md): auth.neocron.org, cdn.neocron.org, neocron.org.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Channel is a game release track exposed in the launcher footer dropdown.
type Channel struct {
	ID       string `json:"id"`       // e.g. "ncc-pts"
	Name     string `json:"name"`     // e.g. "Neocron Public Test"
	Disabled bool   `json:"disabled"` // shown but not selectable
}

// Config holds all launcher configuration persisted to config.json.
type Config struct {
	// Content / install
	InstallDir string `json:"installDir"` // where game files live
	GameExe    string `json:"gameExe"`    // launched binary, "neocron.exe"

	// Service endpoints (overridable for private shards / testing)
	AuthBaseURL string `json:"authBaseUrl"` // https://auth.neocron.org
	CDNBaseURL  string `json:"cdnBaseUrl"`  // https://cdn.neocron.org (content blobs)
	APIBaseURL  string `json:"apiBaseUrl"`  // https://areamc5.neocron.org (manifest/news API)
	ServerKey   string `json:"serverKey"`   // static content-API key (path segment; no auth)
	BannerURL   string `json:"bannerUrl"`   // https://neocron.org/api/launcher

	// Channels
	Channels  []Channel `json:"channels"`
	Channel   string    `json:"channel"`   // selected channel id
	UserAgent string    `json:"userAgent"` // sent on API/CDN requests

	// Account selection (Discord session itself is stored by pkg/auth)
	SelectedAccountID string `json:"selectedAccountId"`

	// Behavior
	PostLaunch string `json:"postLaunch"` // "close" | "taskbar" | "tray"

	// Runtime (Linux launches the win32 client under Proton/Wine)
	RuntimeMode   string `json:"runtimeMode"`   // "proton" | "wine" | "native"
	ProtonPath    string `json:"protonPath"`    // selected Proton build (empty = auto)
	ProtonVersion string `json:"protonVersion"` // display version
	PrefixPath    string `json:"prefixPath"`    // WINEPREFIX (empty = default)

	EnableDXVK     bool `json:"enableDxvk"`
	EnableGameMode bool `json:"enableGameMode"`
	EnableMangoHud bool `json:"enableMangoHud"`

	LaunchArgs string   `json:"launchArgs"`
	WineDebug  string   `json:"wineDebug"`
	ExtraEnv   []string `json:"extraEnv"`

	configPath string
}

// DefaultConfig returns config with defaults for Neocron Classic.
func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()

	runtimeMode := "proton"
	if runtime.GOOS == "windows" {
		runtimeMode = "native"
	}

	postLaunch := "close"
	if runtime.GOOS != "windows" {
		// Closing the launcher under Wine/Proton can take the game down with
		// it; the official launcher disables close-on-launch there.
		postLaunch = "taskbar"
	}

	return &Config{
		InstallDir:  filepath.Join(home, "Games", "Neocron Classic"),
		GameExe:     "Neocron.exe",
		AuthBaseURL: "https://auth.neocron.org",
		CDNBaseURL:  "https://cdn.neocron.org",
		APIBaseURL:  "https://areamc5.neocron.org",
		ServerKey:   "a96f4803d335e41c23486a43ea22a2bf5226cb8a2501da2b25867bccf42be6e1",
		BannerURL:   "https://neocron.org/api/launcher",
		Channels: []Channel{
			{ID: "ncc-pts", Name: "Neocron Public Test"},
			{ID: "ncc-retail", Name: "Neocron Retail", Disabled: true},
		},
		Channel:        "ncc-pts",
		UserAgent:      "NeocronLauncher-cpp/2.2.6",
		PostLaunch:     postLaunch,
		RuntimeMode:    runtimeMode,
		EnableDXVK:     true,
		EnableGameMode: runtime.GOOS == "linux",
	}
}

// ConfigDir returns the launcher's config directory.
func ConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir, _ = os.UserHomeDir()
	}
	return filepath.Join(dir, "neocron-classic-launcher")
}

// ConfigPath returns the path to config.json.
func ConfigPath() string { return filepath.Join(ConfigDir(), "config.json") }

// Load reads config from disk, returning defaults if absent. Missing fields in
// an older file are backfilled from defaults.
func Load() (*Config, error) {
	path := ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			cfg.configPath = path
			return cfg, nil
		}
		return nil, err
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	cfg.configPath = path
	if len(cfg.Channels) == 0 {
		cfg.Channels = DefaultConfig().Channels
	}
	return cfg, nil
}

// Save writes config.json (creating the directory).
func (c *Config) Save() error {
	path := c.configPath
	if path == "" {
		path = ConfigPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ClientDir returns the per-channel install directory (where the manifest lays
// out the game tree): <InstallDir>/clients/<channel>.
func (c *Config) ClientDir() string {
	return filepath.Join(c.InstallDir, "clients", c.Channel)
}

// GameExePath returns the configured absolute path to the game binary for the
// active channel (may not match on-disk casing — see ResolveGameExe).
func (c *Config) GameExePath() string { return filepath.Join(c.ClientDir(), c.GameExe) }

// ResolveGameExe returns the actual path to the game binary, tolerating a
// case-mismatch between GameExe and the on-disk name. The manifest ships
// "Neocron.exe" but older configs saved "neocron.exe", and Linux filesystems
// are case-sensitive — so fall back to a case-insensitive scan of the client dir.
func (c *Config) ResolveGameExe() string {
	exact := c.GameExePath()
	if _, err := os.Stat(exact); err == nil {
		return exact
	}
	entries, err := os.ReadDir(c.ClientDir())
	if err != nil {
		return exact
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(e.Name(), c.GameExe) {
			return filepath.Join(c.ClientDir(), e.Name())
		}
	}
	return exact
}

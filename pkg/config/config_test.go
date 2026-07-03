package config

import (
	"path/filepath"
	"testing"
)

// The game is laid out per-channel: <InstallDir>/clients/<channel>/Neocron.exe,
// and Neocron.exe must run with that dir as its working directory (data.pak /
// data/ / ini/ are resolved relative to it). See docs/RE_LAUNCHER.md §6.11.
func TestClientDirAndGameExePath(t *testing.T) {
	c := DefaultConfig()
	c.InstallDir = "/games/nc"
	c.Channel = "ncc-pts"
	c.GameExe = "Neocron.exe"

	wantDir := filepath.FromSlash("/games/nc/clients/ncc-pts")
	if got := c.ClientDir(); got != wantDir {
		t.Errorf("ClientDir = %q, want %q", got, wantDir)
	}
	wantExe := filepath.Join(wantDir, "Neocron.exe")
	if got := c.GameExePath(); got != wantExe {
		t.Errorf("GameExePath = %q, want %q", got, wantExe)
	}

	// Switching channel moves both.
	c.Channel = "ncc-retail"
	if got := c.ClientDir(); got != filepath.FromSlash("/games/nc/clients/ncc-retail") {
		t.Errorf("ClientDir after channel switch = %q", got)
	}
}

func TestDefaultsHaveManifestEndpoint(t *testing.T) {
	c := DefaultConfig()
	if c.APIBaseURL != "https://areamc5.neocron.org" {
		t.Errorf("APIBaseURL = %q", c.APIBaseURL)
	}
	if len(c.ServerKey) != 64 {
		t.Errorf("ServerKey should be the 64-char static key, got %d chars", len(c.ServerKey))
	}
}

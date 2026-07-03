package addon

import (
	"os"
	"path/filepath"
	"testing"
)

// Network + 7z + reshade.me/github. Gated behind NC1_LIVE_INSTALL=1 so it never
// runs in CI. Proves the whole install pipeline against the published repo.
func TestLiveInstallReShade(t *testing.T) {
	if os.Getenv("NC1_LIVE_INSTALL") != "1" {
		t.Skip("set NC1_LIVE_INSTALL=1 to run the live install")
	}
	install := t.TempDir()
	data := t.TempDir()
	m := NewManager(install)
	m.DataDir = data // keep it out of the real ~/.local/share

	err := m.InstallFromRepo("https://github.com/igwtech/neocron-classic-reshade",
		func(p DownloadProgress) { t.Logf("[%s] %.0f%% %s", p.Status, p.Percent, p.Message) })
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// The stamped client dir must contain the proxy, config, preset, and shaders.
	for _, rel := range []string{"dxgi.dll", "ReShade.ini", "NeocronClassic.ini"} {
		if _, err := os.Stat(filepath.Join(install, rel)); err != nil {
			t.Errorf("missing stamped file %s: %v", rel, err)
		}
	}
	shaders, _ := filepath.Glob(filepath.Join(install, "reshade-shaders", "Shaders", "*.fx"))
	if len(shaders) == 0 {
		t.Errorf("no shaders stamped into reshade-shaders/Shaders")
	}
	t.Logf("stamped %d shaders; dxgi.dll present", len(shaders))

	list, _ := m.ListInstalled()
	if len(list) != 1 || list[0].ID != "neocron-classic-reshade" || !list[0].Enabled {
		t.Errorf("unexpected installed state: %+v", list)
	}
	if o := m.EnabledDLLOverrides(); len(o) != 1 || o[0] != "dxgi" {
		t.Errorf("expected dxgi override, got %v", o)
	}
}

// Tier 0: the Quality addon requires the base. Installing it alone must fail;
// base-then-quality must stack (qUINT shaders + quality preset stamped, base
// dxgi.dll preserved). Gated behind NC1_LIVE_INSTALL=1.
func TestLiveInstallQualityChain(t *testing.T) {
	if os.Getenv("NC1_LIVE_INSTALL") != "1" {
		t.Skip("set NC1_LIVE_INSTALL=1 to run the live install")
	}
	install := t.TempDir()
	m := NewManager(install)
	m.DataDir = t.TempDir()

	// Quality alone must be refused (requires the base).
	if err := m.InstallFromRepo("https://github.com/igwtech/neocron-classic-reshade-quality", nil); err == nil {
		t.Fatalf("installing quality without base should fail (requires chain)")
	}

	// Base, then quality.
	if err := m.InstallFromRepo("https://github.com/igwtech/neocron-classic-reshade", nil); err != nil {
		t.Fatalf("base install failed: %v", err)
	}
	if err := m.InstallFromRepo("https://github.com/igwtech/neocron-classic-reshade-quality",
		func(p DownloadProgress) { t.Logf("[%s] %.0f%% %s", p.Status, p.Percent, p.Message) }); err != nil {
		t.Fatalf("quality install failed: %v", err)
	}

	// qUINT AO/SSR shaders stamped, plus base's dxgi.dll still present.
	for _, rel := range []string{
		"dxgi.dll",
		"NeocronClassic-Quality.ini",
		filepath.Join("reshade-shaders", "Shaders", "qUINT_mxao.fx"),
		filepath.Join("reshade-shaders", "Shaders", "qUINT_ssr.fx"),
	} {
		if _, err := os.Stat(filepath.Join(install, rel)); err != nil {
			t.Errorf("missing stamped file %s: %v", rel, err)
		}
	}
	// Quality (higher priority) owns ReShade.ini -> points at the quality preset.
	ini, _ := os.ReadFile(filepath.Join(install, "ReShade.ini"))
	if !contains(string(ini), "NeocronClassic-Quality.ini") {
		t.Errorf("ReShade.ini should select the quality preset, got:\n%s", ini)
	}
	list, _ := m.ListInstalled()
	if len(list) != 2 {
		t.Errorf("expected 2 installed addons, got %d", len(list))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

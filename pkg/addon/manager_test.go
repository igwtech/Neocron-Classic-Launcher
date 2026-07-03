package addon

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestEnabledDLLOverrides(t *testing.T) {
	dataDir := t.TempDir()

	s := &state{Addons: []InstalledAddon{
		{
			ID:      "graphics-pack",
			Enabled: true,
			Manifest: AddonManifest{
				ID:               "graphics-pack",
				WineDLLOverrides: []string{"d3d8", "dxgi"},
			},
		},
		{
			ID:      "audio-pack",
			Enabled: true,
			Manifest: AddonManifest{
				ID:               "audio-pack",
				WineDLLOverrides: []string{"dsound"},
			},
		},
		{
			ID:      "disabled-pack",
			Enabled: false,
			Manifest: AddonManifest{
				ID:               "disabled-pack",
				WineDLLOverrides: []string{"d3d9"},
			},
		},
		{
			ID:      "duplicate-pack",
			Enabled: true,
			Manifest: AddonManifest{
				ID:               "duplicate-pack",
				WineDLLOverrides: []string{"D3D8", " DXGI ", ""},
			},
		},
	}}
	if err := saveState(dataDir, s); err != nil {
		t.Fatal(err)
	}

	m := &Manager{DataDir: dataDir}
	got := m.EnabledDLLOverrides()
	sort.Strings(got)

	want := []string{"d3d8", "dsound", "dxgi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnabledDLLOverrides() = %v, want %v (disabled-pack must not appear; case+space dedup must apply)", got, want)
	}
}

func TestEnabledDLLOverrides_emptyState(t *testing.T) {
	m := &Manager{DataDir: t.TempDir()}
	if got := m.EnabledDLLOverrides(); len(got) != 0 {
		t.Errorf("expected no overrides for empty state, got %v", got)
	}
}

func TestEnabledDLLOverrides_noEnabledAddons(t *testing.T) {
	dataDir := t.TempDir()
	s := &state{Addons: []InstalledAddon{
		{
			ID:          "x",
			Enabled:     false,
			InstalledAt: time.Now(),
			Manifest:    AddonManifest{ID: "x", WineDLLOverrides: []string{"d3d8"}},
		},
	}}
	saveState(dataDir, s)

	m := &Manager{DataDir: dataDir}
	if got := m.EnabledDLLOverrides(); len(got) != 0 {
		t.Errorf("disabled addon must contribute nothing, got %v", got)
	}
}

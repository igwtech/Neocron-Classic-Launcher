package addon

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testFixture builds a Manager wired to two temp dirs, plus helpers for
// populating addon caches and inspecting the install dir.
type testFixture struct {
	t          *testing.T
	dataDir    string
	installDir string
	mgr        *Manager
}

func newFixture(t *testing.T) *testFixture {
	t.Helper()
	f := &testFixture{
		t:          t,
		dataDir:    t.TempDir(),
		installDir: t.TempDir(),
	}
	f.mgr = &Manager{
		DataDir:    f.dataDir,
		InstallDir: f.installDir,
		Logger:     log.New(os.Stderr, "[addon-test] ", 0),
	}
	return f
}

// writeInstall puts a file in the install dir (the "game" original).
func (f *testFixture) writeInstall(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.installDir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		f.t.Fatal(err)
	}
}

// addAddon writes an addon's cache, persists it to state with the given
// priority + enabled flag, and captures pristine snapshots for its files.
func (f *testFixture) addAddon(id string, priority int, enabled bool, files map[string]string, requires, conflicts []string) {
	f.t.Helper()
	cacheDir := addonFilesDir(f.dataDir, id)
	for rel, content := range files {
		p := filepath.Join(cacheDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			f.t.Fatal(err)
		}
	}

	s, err := loadState(f.dataDir)
	if err != nil {
		f.t.Fatal(err)
	}
	s.Addons = append(s.Addons, InstalledAddon{
		ID:          id,
		Enabled:     enabled,
		InstalledAt: time.Now().Add(time.Duration(priority) * time.Second),
		Priority:    priority,
		Manifest: AddonManifest{
			ID:        id,
			Requires:  requires,
			Conflicts: conflicts,
		},
	})

	paths, err := f.mgr.pathsTouched(id)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.mgr.captureSnapshots(s, paths); err != nil {
		f.t.Fatal(err)
	}
	if err := saveState(f.dataDir, s); err != nil {
		f.t.Fatal(err)
	}
}

// readInstall returns the install-dir file content, or "" if absent.
func (f *testFixture) readInstall(rel string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.installDir, rel))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *testFixture) recompute() {
	f.t.Helper()
	s, err := loadState(f.dataDir)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.mgr.recomputeStack(s); err != nil {
		f.t.Fatal(err)
	}
	if err := saveState(f.dataDir, s); err != nil {
		f.t.Fatal(err)
	}
}

// --- Tests ---

// Higher priority wins on overlapping files.
func TestRecomputeStack_higherPriorityWins(t *testing.T) {
	f := newFixture(t)
	f.writeInstall("data/cfg.txt", "ORIG")

	f.addAddon("low", 1, true, map[string]string{"data/cfg.txt": "FROM-LOW"}, nil, nil)
	f.addAddon("high", 2, true, map[string]string{"data/cfg.txt": "FROM-HIGH"}, nil, nil)

	f.recompute()
	if got := f.readInstall("data/cfg.txt"); got != "FROM-HIGH" {
		t.Errorf("expected high-priority addon to win, got %q", got)
	}
}

// Disabling the top of a stack restores the layer beneath, not pristine.
func TestRecomputeStack_disableRestoresPriorLayer(t *testing.T) {
	f := newFixture(t)
	f.writeInstall("d3d8.dll", "PRISTINE-DLL")
	f.addAddon("dgvoodoo", 1, true, map[string]string{"d3d8.dll": "DGVOODOO-DLL"}, nil, nil)
	f.addAddon("override", 2, true, map[string]string{"d3d8.dll": "OVERRIDE-DLL"}, nil, nil)

	f.recompute()
	if got := f.readInstall("d3d8.dll"); got != "OVERRIDE-DLL" {
		t.Fatalf("setup: expected OVERRIDE-DLL on top, got %q", got)
	}

	if err := f.mgr.Disable("override"); err != nil {
		t.Fatal(err)
	}
	if got := f.readInstall("d3d8.dll"); got != "DGVOODOO-DLL" {
		t.Errorf("disabling top layer should restore prior layer (DGVOODOO-DLL), got %q", got)
	}

	if err := f.mgr.Disable("dgvoodoo"); err != nil {
		t.Fatal(err)
	}
	if got := f.readInstall("d3d8.dll"); got != "PRISTINE-DLL" {
		t.Errorf("disabling all addons should restore pristine, got %q", got)
	}
}

// An addon that adds a new file (PristineAbsent) should have it removed when
// the addon is disabled.
func TestRecomputeStack_pristineAbsentRemoval(t *testing.T) {
	f := newFixture(t)
	f.addAddon("addon-a", 1, true, map[string]string{"new-file.dll": "ADDON-CONTENT"}, nil, nil)
	f.recompute()

	if got := f.readInstall("new-file.dll"); got != "ADDON-CONTENT" {
		t.Fatalf("setup: expected ADDON-CONTENT, got %q", got)
	}

	if err := f.mgr.Disable("addon-a"); err != nil {
		t.Fatal(err)
	}
	if got := f.readInstall("new-file.dll"); got != "" {
		t.Errorf("file added by addon should be removed on disable, got %q", got)
	}
}

// SetPriority changes the winner without losing earlier-stamp content.
func TestRecomputeStack_reorderFlipsWinner(t *testing.T) {
	f := newFixture(t)
	f.writeInstall("x", "PRISTINE")
	f.addAddon("a", 1, true, map[string]string{"x": "A"}, nil, nil)
	f.addAddon("b", 2, true, map[string]string{"x": "B"}, nil, nil)

	f.recompute()
	if got := f.readInstall("x"); got != "B" {
		t.Fatalf("setup: B (pri 2) should win, got %q", got)
	}

	// Promote A above B.
	if err := f.mgr.SetPriority("a", 99); err != nil {
		t.Fatal(err)
	}
	if got := f.readInstall("x"); got != "A" {
		t.Errorf("after promoting A, A should win, got %q", got)
	}
}

// Auto-enable transitive deps when enabling a child addon.
func TestEnable_autoEnablesRequires(t *testing.T) {
	f := newFixture(t)
	f.addAddon("base", 1, false, map[string]string{"a.dll": "BASE"}, nil, nil)
	f.addAddon("addon-with-dep", 2, false, map[string]string{"b.dll": "DEP"}, []string{"base"}, nil)

	if err := f.mgr.Enable("addon-with-dep"); err != nil {
		t.Fatalf("enable should succeed and auto-enable base: %v", err)
	}

	addons, _ := f.mgr.ListInstalled()
	enabled := map[string]bool{}
	for _, a := range addons {
		enabled[a.ID] = a.Enabled
	}
	if !enabled["base"] {
		t.Errorf("base must be auto-enabled, got %+v", enabled)
	}
	if !enabled["addon-with-dep"] {
		t.Errorf("addon-with-dep must be enabled, got %+v", enabled)
	}
}

// Enabling fails if a required addon is not installed.
func TestEnable_missingRequireErrors(t *testing.T) {
	f := newFixture(t)
	f.addAddon("with-missing-dep", 1, false, map[string]string{"x": "Y"}, []string{"never-installed"}, nil)

	err := f.mgr.Enable("with-missing-dep")
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("expected 'not installed' error, got %v", err)
	}
}

// Enabling fails when a conflict is enabled.
func TestEnable_conflictRefused(t *testing.T) {
	f := newFixture(t)
	f.addAddon("a", 1, true, map[string]string{"x": "A"}, nil, nil)
	f.addAddon("b", 2, false, map[string]string{"x": "B"}, nil, []string{"a"})

	err := f.mgr.Enable("b")
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Errorf("expected conflict error, got %v", err)
	}
}

// Reverse-conflict: enabling X is refused if an already-enabled Y lists X as a conflict.
func TestEnable_reverseConflictRefused(t *testing.T) {
	f := newFixture(t)
	f.addAddon("with-conflicts", 1, true, map[string]string{"x": "A"}, nil, []string{"target"})
	f.addAddon("target", 2, false, map[string]string{"y": "B"}, nil, nil)

	err := f.mgr.Enable("target")
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Errorf("expected reverse-conflict error, got %v", err)
	}
}

// Disable refused while a dependent is enabled.
func TestDisable_dependentBlocks(t *testing.T) {
	f := newFixture(t)
	f.addAddon("base", 1, true, map[string]string{"a": "A"}, nil, nil)
	f.addAddon("dep", 2, true, map[string]string{"b": "B"}, []string{"base"}, nil)

	err := f.mgr.Disable("base")
	if err == nil || !strings.Contains(err.Error(), "required by") {
		t.Errorf("expected 'required by' error, got %v", err)
	}

	// Disabling the dependent first should let us disable base.
	if err := f.mgr.Disable("dep"); err != nil {
		t.Fatal(err)
	}
	if err := f.mgr.Disable("base"); err != nil {
		t.Errorf("disable base after dep disabled should succeed, got %v", err)
	}
}

// Uninstall refused while another addon requires it (even if disabled).
func TestUninstall_dependentBlocks(t *testing.T) {
	f := newFixture(t)
	f.addAddon("base", 1, true, map[string]string{"a": "A"}, nil, nil)
	f.addAddon("dep", 2, false, map[string]string{"b": "B"}, []string{"base"}, nil)

	err := f.mgr.Uninstall("base")
	if err == nil || !strings.Contains(err.Error(), "required by") {
		t.Errorf("expected 'required by' error, got %v", err)
	}
}

// Cycle detection.
func TestDetectRequiresCycle(t *testing.T) {
	addons := []InstalledAddon{
		{ID: "a", Manifest: AddonManifest{ID: "a", Requires: []string{"b"}}},
		{ID: "b", Manifest: AddonManifest{ID: "b", Requires: []string{"c"}}},
		{ID: "c", Manifest: AddonManifest{ID: "c", Requires: []string{"a"}}},
	}

	if err := detectRequiresCycle("a", addons); err == nil {
		t.Errorf("expected cycle error")
	}

	// Acyclic graph should pass.
	addons[2].Manifest.Requires = nil
	if err := detectRequiresCycle("a", addons); err != nil {
		t.Errorf("acyclic graph should pass, got %v", err)
	}
}

// Uninstall must restore the addon's files from pristine — not leave them
// stamped after the addon is gone.
func TestUninstall_restoresFilesFromPristine(t *testing.T) {
	f := newFixture(t)
	f.writeInstall("data.txt", "ORIGINAL")
	f.addAddon("modder", 1, true, map[string]string{"data.txt": "MODDED"}, nil, nil)
	f.recompute()
	if got := f.readInstall("data.txt"); got != "MODDED" {
		t.Fatalf("setup: expected MODDED, got %q", got)
	}

	if err := f.mgr.Uninstall("modder"); err != nil {
		t.Fatal(err)
	}
	if got := f.readInstall("data.txt"); got != "ORIGINAL" {
		t.Errorf("uninstall must restore pristine, got %q", got)
	}
}

// An addon that adds a file (PristineAbsent) — the file must be removed on
// uninstall, not left behind.
func TestUninstall_removesAddedFile(t *testing.T) {
	f := newFixture(t)
	f.addAddon("addon", 1, true, map[string]string{"new.dll": "ADDED"}, nil, nil)
	f.recompute()
	if got := f.readInstall("new.dll"); got != "ADDED" {
		t.Fatalf("setup: expected ADDED, got %q", got)
	}

	if err := f.mgr.Uninstall("addon"); err != nil {
		t.Fatal(err)
	}
	if got := f.readInstall("new.dll"); got != "" {
		t.Errorf("uninstall must remove addon-added file, got %q", got)
	}
}

// MissingExpected is recomputed live each call: dropping the file in clears
// the entry without needing reinstall.
func TestMissingExpected_livenessAcrossFilesystemChanges(t *testing.T) {
	f := newFixture(t)

	// Addon declares two manual files. Neither exists yet.
	s, _ := loadState(f.dataDir)
	s.Addons = append(s.Addons, InstalledAddon{
		ID:      "graphics-pack",
		Enabled: true,
		Manifest: AddonManifest{
			ID:      "graphics-pack",
			Expects: []string{"dxgi.dll", "reshade-shaders/Shaders"},
		},
	})
	saveState(f.dataDir, s)

	missing := f.mgr.MissingExpected()
	if got := missing["graphics-pack"]; len(got) != 2 {
		t.Fatalf("expected 2 missing paths, got %v", got)
	}

	// User completes manual install of dxgi.dll.
	f.writeInstall("dxgi.dll", "RESHADE-USER-PROVIDED")

	missing = f.mgr.MissingExpected()
	if got := missing["graphics-pack"]; len(got) != 1 || got[0] != "reshade-shaders/Shaders" {
		t.Errorf("after providing dxgi.dll, only Shaders should be missing, got %v", got)
	}

	// Provide the shader dir too (as a directory).
	if err := os.MkdirAll(filepath.Join(f.installDir, "reshade-shaders/Shaders"), 0755); err != nil {
		t.Fatal(err)
	}

	missing = f.mgr.MissingExpected()
	if got, ok := missing["graphics-pack"]; ok {
		t.Errorf("after providing all expects, addon should not appear in missing map, got %v", got)
	}
}

// Update bypasses the dependent check — replacing an addon in place must not
// be blocked by other addons that require it.
func TestUpdate_allowsDependents(t *testing.T) {
	f := newFixture(t)
	f.addAddon("base", 1, true, map[string]string{"a": "BASE-OLD"}, nil, nil)
	f.addAddon("dep", 2, true, map[string]string{"b": "DEP"}, []string{"base"}, nil)

	// Public Uninstall is blocked.
	if err := f.mgr.Uninstall("base"); err == nil {
		t.Errorf("public Uninstall should be blocked by dependents")
	}
	// Internal uninstallLocked with allowDependents=true must succeed.
	f.mgr.mu.Lock()
	err := f.mgr.uninstallLocked("base", true)
	f.mgr.mu.Unlock()
	if err != nil {
		t.Errorf("uninstallLocked(allowDependents=true) should succeed, got %v", err)
	}
}

// Update preserves the user's priority and disabled state across the
// in-place uninstall + reinstall. We can't run the full network round-trip
// in a unit test, so we simulate it: uninstall + re-add (as a fresh install
// would, with default priority/enabled) + the priority-restoration step
// that Update performs.
func TestUpdate_preservesPriorityAndEnabled(t *testing.T) {
	f := newFixture(t)
	f.writeInstall("data", "ORIG")
	// User installed with default settings, then customized:
	//   priority = 42 (high, manually promoted)
	//   enabled  = false (manually disabled while testing)
	f.addAddon("x", 42, false, map[string]string{"data": "MOD-V1"}, nil, nil)

	// Simulate Update's first half: uninstall, allowing dependents.
	f.mgr.mu.Lock()
	if err := f.mgr.uninstallLocked("x", true); err != nil {
		f.mgr.mu.Unlock()
		t.Fatal(err)
	}
	f.mgr.mu.Unlock()

	// Simulate Update's second half: a fresh install would assign default
	// priority (highestPriority+1 = 0 since list is empty) and Enabled=true.
	f.addAddon("x", 0, true, map[string]string{"data": "MOD-V2"}, nil, nil)

	// Now exercise the bug fix — the priority/enabled restoration step.
	s, err := loadState(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	i := indexByID(s.Addons, "x")
	if i < 0 {
		t.Fatal("addon should be reinstalled")
	}
	s.Addons[i].Priority = 42  // restore user's manual priority
	s.Addons[i].Enabled = false // restore user's disabled state
	if err := f.mgr.recomputeStack(s); err != nil {
		t.Fatal(err)
	}
	if err := saveState(f.dataDir, s); err != nil {
		t.Fatal(err)
	}

	// Verify: priority + enabled survived; install dir reflects disabled state
	// (back to pristine, not stamped with v2).
	addons, _ := f.mgr.ListInstalled()
	got := addons[indexByID(addons, "x")]
	if got.Priority != 42 {
		t.Errorf("priority should be preserved (42), got %d", got.Priority)
	}
	if got.Enabled {
		t.Errorf("enabled state should be preserved (false), got true")
	}
	if disk := f.readInstall("data"); disk != "ORIG" {
		t.Errorf("disabled-after-update install dir should be pristine ORIG, got %q", disk)
	}
}

// Disabled-conflict installation is allowed; only enabled conflicts block.
func TestValidateConflicts_disabledConflictAllowsInstall(t *testing.T) {
	addons := []InstalledAddon{
		{ID: "a", Enabled: false, Manifest: AddonManifest{ID: "a"}},
	}
	s := &state{Addons: addons}
	manifest := AddonManifest{ID: "b", Conflicts: []string{"a"}}

	// checkInstalled=false (install-time semantics): disabled conflict is fine.
	if err := validateConflicts(manifest, s, false); err != nil {
		t.Errorf("disabled conflict should not block install: %v", err)
	}

	// Once 'a' is enabled, install of 'b' is refused.
	s.Addons[0].Enabled = true
	if err := validateConflicts(manifest, s, false); err == nil {
		t.Errorf("enabled conflict should block install")
	}
}

func TestValidateManifest(t *testing.T) {
	cases := []struct {
		name      string
		m         AddonManifest
		wantError string // substring; "" means must succeed
	}{
		{
			name:      "missing id",
			m:         AddonManifest{Name: "x", Version: "1", Files: []FileEntry{{Src: "a", Dst: "a"}}},
			wantError: "'id' is required",
		},
		{
			name:      "missing name",
			m:         AddonManifest{ID: "x", Version: "1", Files: []FileEntry{{Src: "a", Dst: "a"}}},
			wantError: "'name' is required",
		},
		{
			name:      "missing version",
			m:         AddonManifest{ID: "x", Name: "X", Files: []FileEntry{{Src: "a", Dst: "a"}}},
			wantError: "'version' is required",
		},
		{
			name:      "no files and no overrides",
			m:         AddonManifest{ID: "x", Name: "X", Version: "1"},
			wantError: "must declare at least one",
		},
		{
			name:      "self-require",
			m:         AddonManifest{ID: "x", Name: "X", Version: "1", Requires: []string{"x"}, Files: []FileEntry{{Src: "a", Dst: "a"}}},
			wantError: "lists itself in 'requires'",
		},
		{
			name:      "self-conflict",
			m:         AddonManifest{ID: "x", Name: "X", Version: "1", Conflicts: []string{"x"}, Files: []FileEntry{{Src: "a", Dst: "a"}}},
			wantError: "lists itself in 'conflicts'",
		},
		{
			name:      "duplicate require",
			m:         AddonManifest{ID: "x", Name: "X", Version: "1", Requires: []string{"a", "a"}, Files: []FileEntry{{Src: "a", Dst: "a"}}},
			wantError: "duplicate entry in 'requires'",
		},
		{
			name:      "path traversal in dst",
			m:         AddonManifest{ID: "x", Name: "X", Version: "1", Files: []FileEntry{{Src: "a", Dst: "../etc/passwd"}}},
			wantError: "must not contain '..'",
		},
		{
			name:      "missing src or dst",
			m:         AddonManifest{ID: "x", Name: "X", Version: "1", Files: []FileEntry{{Src: "", Dst: "a"}}},
			wantError: "need both 'src' and 'dst'",
		},
		{
			name: "valid wrapper-only manifest (no files, just overrides)",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				WineDLLOverrides: []string{"d3d8"},
			},
			wantError: "",
		},
		{
			name: "expects path traversal rejected",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Files:   []FileEntry{{Src: "a", Dst: "a"}},
				Expects: []string{"../etc/passwd"},
			},
			wantError: "must not contain '..'",
		},
		{
			name: "expects empty string rejected",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Files:   []FileEntry{{Src: "a", Dst: "a"}},
				Expects: []string{""},
			},
			wantError: "must not be empty",
		},
		{
			name: "valid expects (e.g. graphics pack)",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Files:   []FileEntry{{Src: "a", Dst: "a"}},
				Expects: []string{"dxgi.dll", "reshade-shaders/Shaders"},
			},
			wantError: "",
		},
		{
			name: "valid full manifest",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Files:    []FileEntry{{Src: "a", Dst: "a"}},
				Requires: []string{"y"},
			},
			wantError: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateManifest(tc.m)
			if tc.wantError == "" {
				if err != nil {
					t.Errorf("expected success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tc.wantError)
				return
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("expected error containing %q, got %q", tc.wantError, err.Error())
			}
		})
	}
}

// Post-update flow: PrepareForUpdate strips addon stamps, FinishAfterUpdate
// snapshots the new pristine and re-stamps.
func TestUpdateLifecycle_refreshesPristine(t *testing.T) {
	f := newFixture(t)
	f.writeInstall("game.dat", "v1")
	f.addAddon("mod", 1, true, map[string]string{"game.dat": "MODDED"}, nil, nil)
	f.recompute()

	if got := f.readInstall("game.dat"); got != "MODDED" {
		t.Fatalf("setup: expected MODDED, got %q", got)
	}

	// Simulate CDN update flow.
	if err := f.mgr.PrepareForUpdate(); err != nil {
		t.Fatal(err)
	}
	if got := f.readInstall("game.dat"); got != "v1" {
		t.Fatalf("PrepareForUpdate should restore pristine v1, got %q", got)
	}

	// CDN updater overwrites with v2.
	f.writeInstall("game.dat", "v2")

	if err := f.mgr.FinishAfterUpdate(); err != nil {
		t.Fatal(err)
	}

	// Install dir should now have the addon stamp on top of v2.
	if got := f.readInstall("game.dat"); got != "MODDED" {
		t.Errorf("after Finish, expected MODDED on top, got %q", got)
	}

	// Disabling the mod should now restore v2 (not v1) — pristine refreshed.
	if err := f.mgr.Disable("mod"); err != nil {
		t.Fatal(err)
	}
	if got := f.readInstall("game.dat"); got != "v2" {
		t.Errorf("after disable post-update, pristine should be v2, got %q", got)
	}
}

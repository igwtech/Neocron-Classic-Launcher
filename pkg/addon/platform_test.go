package addon

import (
	"reflect"
	"testing"
	"time"
)

func TestPlatformMatches(t *testing.T) {
	saved := goosOverride
	defer func() { goosOverride = saved }()

	cases := []struct {
		name    string
		current string
		allowed []string
		want    bool
	}{
		{"empty allowed = any platform", "linux", nil, true},
		{"empty slice = any platform", "darwin", []string{}, true},
		{"exact match linux", "linux", []string{"linux"}, true},
		{"exact match windows", "windows", []string{"windows"}, true},
		{"multi-platform list, hits", "darwin", []string{"linux", "darwin"}, true},
		{"multi-platform list, miss", "windows", []string{"linux", "darwin"}, false},
		{"case-insensitive", "linux", []string{"Linux"}, true},
		{"whitespace-tolerant", "linux", []string{"  linux  "}, true},
		{"reject mismatched", "linux", []string{"windows"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goosOverride = tc.current
			if got := platformMatches(tc.allowed); got != tc.want {
				t.Errorf("platformMatches(%v) under %q = %v, want %v",
					tc.allowed, tc.current, got, tc.want)
			}
		})
	}
}

func TestEnabledEnvVars_filtersByPlatformAndExpandsTemplate(t *testing.T) {
	saved := goosOverride
	defer func() { goosOverride = saved }()
	goosOverride = "linux"

	dataDir := t.TempDir()
	installDir := "/games/Neocron2"
	m := &Manager{DataDir: dataDir, InstallDir: installDir}

	s := &state{
		Addons: []InstalledAddon{
			{
				ID: "graphics", Enabled: true, InstalledAt: time.Now(),
				Manifest: AddonManifest{
					ID: "graphics", Name: "Graphics", Version: "1",
					EnvVars: map[string]map[string]string{
						"linux": {
							"ENABLE_VKBASALT":      "1",
							"VKBASALT_CONFIG_FILE": "${INSTALL_DIR}/vkBasalt.conf",
						},
						"windows": {
							"NEVER_SET_ON_LINUX": "yes",
						},
					},
				},
			},
			{
				// Disabled addon shouldn't contribute env vars even if it matches
				// the platform. Enable toggles must propagate to env state.
				ID: "disabled", Enabled: false, InstalledAt: time.Now(),
				Manifest: AddonManifest{
					ID: "disabled", Name: "Disabled", Version: "1",
					EnvVars: map[string]map[string]string{
						"linux": {"DISABLED_VAR": "value"},
					},
				},
			},
		},
	}
	if err := saveState(dataDir, s); err != nil {
		t.Fatal(err)
	}

	got, err := m.EnabledEnvVars()
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"ENABLE_VKBASALT":      "1",
		"VKBASALT_CONFIG_FILE": "/games/Neocron2/vkBasalt.conf",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnabledEnvVars():\n  got  %v\n  want %v", got, want)
	}
}

func TestEnabledEnvVars_higherPriorityOverridesSameKey(t *testing.T) {
	saved := goosOverride
	defer func() { goosOverride = saved }()
	goosOverride = "linux"

	dataDir := t.TempDir()
	m := &Manager{DataDir: dataDir, InstallDir: "/x"}

	// Two addons set the same key. enabledInOrder sorts by priority asc, so
	// the higher-priority addon comes LAST in the iteration and wins via
	// last-write semantics — that's what we test.
	s := &state{
		Addons: []InstalledAddon{
			{
				ID: "low", Priority: 1, Enabled: true,
				Manifest: AddonManifest{
					ID: "low", Name: "Low", Version: "1",
					EnvVars: map[string]map[string]string{
						"linux": {"SHARED_KEY": "loser"},
					},
				},
			},
			{
				ID: "high", Priority: 5, Enabled: true,
				Manifest: AddonManifest{
					ID: "high", Name: "High", Version: "1",
					EnvVars: map[string]map[string]string{
						"linux": {"SHARED_KEY": "winner"},
					},
				},
			},
		},
	}
	if err := saveState(dataDir, s); err != nil {
		t.Fatal(err)
	}

	got, _ := m.EnabledEnvVars()
	if got["SHARED_KEY"] != "winner" {
		t.Errorf("higher-priority addon should win for shared key; got %q", got["SHARED_KEY"])
	}
}

func TestValidateManifest_envVarsRejectsBadPlatform(t *testing.T) {
	err := validateManifest(AddonManifest{
		ID: "x", Name: "X", Version: "1",
		EnvVars: map[string]map[string]string{
			"freebsd": {"FOO": "bar"}, // unsupported platform
		},
	})
	if err == nil {
		t.Fatal("expected error for unsupported platform key, got nil")
	}
}

func TestValidateManifest_envVarsAllowsKnownPlatforms(t *testing.T) {
	for _, plat := range []string{"linux", "darwin", "windows"} {
		err := validateManifest(AddonManifest{
			ID: "x", Name: "X", Version: "1",
			EnvVars: map[string]map[string]string{
				plat: {"FOO": "bar"},
			},
		})
		if err != nil {
			t.Errorf("platform %q should be accepted, got: %v", plat, err)
		}
	}
}

func TestValidateManifest_envVarsRejectsEmptyKey(t *testing.T) {
	err := validateManifest(AddonManifest{
		ID: "x", Name: "X", Version: "1",
		EnvVars: map[string]map[string]string{
			"linux": {"": "bar"},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty env var name, got nil")
	}
}

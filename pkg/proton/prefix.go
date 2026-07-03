package proton

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// PrefixStatus describes the state of a Wine/Proton prefix.
type PrefixStatus struct {
	Initialized    bool   `json:"initialized"`
	DepsInstalled  bool   `json:"depsInstalled"`
	Path           string `json:"path"`
	Message        string `json:"message"`
}

// PrefixManager handles Wine/Proton prefix creation and configuration.
type PrefixManager struct {
	PrefixPath string
}

// NewPrefixManager creates a prefix manager for the given path.
func NewPrefixManager(prefixPath string) *PrefixManager {
	if prefixPath == "" {
		prefixPath = defaultPrefixPath()
	}
	return &PrefixManager{PrefixPath: prefixPath}
}

func defaultPrefixPath() string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "neocron-launcher", "prefix")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "neocron-launcher", "prefix")
}

// GetStatus checks whether the prefix is initialized and usable.
func (pm *PrefixManager) GetStatus() PrefixStatus {
	initialized := false

	systemReg := filepath.Join(pm.PrefixPath, "pfx", "system.reg")
	if _, err := os.Stat(systemReg); err == nil {
		initialized = true
	}
	systemReg2 := filepath.Join(pm.PrefixPath, "system.reg")
	if _, err := os.Stat(systemReg2); err == nil {
		initialized = true
	}

	depsInstalled := false
	depsMarker := filepath.Join(pm.PrefixPath, ".neocron-deps-installed")
	if _, err := os.Stat(depsMarker); err == nil {
		depsInstalled = true
	}

	msg := "Prefix needs setup"
	if initialized && depsInstalled {
		msg = "Prefix ready"
	} else if initialized {
		msg = "Prefix initialized, dependencies not installed"
	}

	return PrefixStatus{
		Initialized:   initialized,
		DepsInstalled: depsInstalled,
		Path:          pm.PrefixPath,
		Message:       msg,
	}
}

// Setup initializes the prefix and installs Neocron dependencies.
func (pm *PrefixManager) Setup(protonBuildPath string, onOutput func(string)) error {
	if err := os.MkdirAll(pm.PrefixPath, 0755); err != nil {
		return fmt.Errorf("create prefix dir: %w", err)
	}

	emit := func(msg string) {
		if onOutput != nil {
			onOutput(msg)
		}
	}

	// Step 1: Initialize prefix
	if !pm.GetStatus().Initialized {
		protonScript := GetProtonScript(protonBuildPath)
		if protonScript != "" {
			if err := pm.setupViaProton(protonScript, protonBuildPath, onOutput); err != nil {
				return err
			}
		} else {
			wineBin := GetBuildWineBinary(protonBuildPath)
			if wineBin == "" {
				if path, err := exec.LookPath("wine"); err == nil {
					wineBin = path
				} else {
					return fmt.Errorf("no Proton or Wine binary found in %s", protonBuildPath)
				}
			}
			if err := pm.setupViaWine(wineBin, onOutput); err != nil {
				return err
			}
		}
	} else {
		emit("Prefix already initialized, skipping...")
	}

	// Step 2: Set Windows version to Win98
	emit("Setting Windows version to Windows 98...")
	if err := pm.applyWin98Registry(protonBuildPath); err != nil {
		emit(fmt.Sprintf("Warning: could not set Windows version: %v", err))
	}

	// Step 3: Install winetricks dependencies
	if !pm.GetStatus().DepsInstalled {
		emit("Installing Neocron dependencies (corefonts, vcrun6, mfc42)...")
		if err := pm.InstallDependencies(protonBuildPath, onOutput); err != nil {
			emit(fmt.Sprintf("Warning: dependency installation failed: %v", err))
			// Don't fail the whole setup — game may still work
		}
	} else {
		emit("Dependencies already installed, skipping...")
	}

	return nil
}

func (pm *PrefixManager) setupViaProton(protonScript, buildPath string, onOutput func(string)) error {
	emit := func(msg string) {
		if onOutput != nil {
			onOutput(msg)
		}
	}

	emit("Initializing prefix via Proton...")

	env := pm.buildProtonEnv(buildPath, nil)

	cmd := exec.Command("python3", protonScript, "run", "cmd", "/c", "echo", "prefix initialized")
	cmd.Env = env
	cmd.Dir = buildPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		emit(fmt.Sprintf("Proton output: %s", string(output)))
		if pm.GetStatus().Initialized {
			emit("Prefix initialized successfully (despite non-zero exit)")
			return nil
		}
		return fmt.Errorf("proton setup failed: %w\n%s", err, string(output))
	}

	emit("Prefix initialized successfully")
	return nil
}

func (pm *PrefixManager) setupViaWine(wineBinary string, onOutput func(string)) error {
	emit := func(msg string) {
		if onOutput != nil {
			onOutput(msg)
		}
	}

	emit("Initializing prefix via Wine...")

	winebootPath := filepath.Join(filepath.Dir(wineBinary), "wineboot")
	if _, err := os.Stat(winebootPath); err != nil {
		winebootPath = "wineboot"
	}

	cmd := exec.Command(winebootPath, "--init")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("WINEPREFIX=%s", pm.PrefixPath),
		"WINEDEBUG=-all,err+module",
	)

	output, err := cmd.CombinedOutput()
	emit(string(output))
	if err != nil {
		if pm.GetStatus().Initialized {
			emit("Prefix initialized successfully (despite non-zero exit)")
			return nil
		}
		return fmt.Errorf("wineboot failed: %w", err)
	}

	emit("Prefix initialized successfully")
	return nil
}

// applyWin98Registry sets Windows 98 compatibility for the game executables.
func (pm *PrefixManager) applyWin98Registry(buildPath string) error {
	regContent := `REGEDIT4

[HKEY_CURRENT_USER\Software\Wine]
"Version"="win98"

[HKEY_CURRENT_USER\Software\Wine\AppDefaults\neocronclient.exe]
"Version"="win98"

[HKEY_CURRENT_USER\Software\Wine\AppDefaults\client.exe]
"Version"="win98"

`
	// Find the actual prefix dir (Proton uses pfx/ subdirectory)
	prefixDir := pm.PrefixPath
	if _, err := os.Stat(filepath.Join(pm.PrefixPath, "pfx", "user.reg")); err == nil {
		prefixDir = filepath.Join(pm.PrefixPath, "pfx")
	}

	tmpFile, err := os.CreateTemp("", "neocron-win98-*.reg")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	tmpFile.WriteString(regContent)
	tmpFile.Close()

	wineBin := GetBuildWineBinary(buildPath)
	if wineBin == "" {
		wineBin = "wine"
	}
	regeditPath := filepath.Join(filepath.Dir(wineBin), "regedit")
	if _, err := os.Stat(regeditPath); err != nil {
		regeditPath = "regedit"
	}

	cmd := exec.Command(regeditPath, tmpFile.Name())
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("WINEPREFIX=%s", prefixDir),
		"WINEDEBUG=-all,err+module",
	)
	return cmd.Run()
}

// InstallDependencies runs winetricks to install Neocron's required libraries.
func (pm *PrefixManager) InstallDependencies(protonBuildPath string, onOutput func(string)) error {
	emit := func(msg string) {
		if onOutput != nil {
			onOutput(msg)
		}
	}

	// Find winetricks
	winetricksPath, err := exec.LookPath("winetricks")
	if err != nil {
		emit("winetricks not found in PATH — attempting to download...")
		winetricksPath, err = pm.downloadWinetricks()
		if err != nil {
			return fmt.Errorf("winetricks not available: %w", err)
		}
	}

	// Find the actual prefix dir
	prefixDir := pm.PrefixPath
	if _, err := os.Stat(filepath.Join(pm.PrefixPath, "pfx", "system.reg")); err == nil {
		prefixDir = filepath.Join(pm.PrefixPath, "pfx")
	}

	// Build environment
	env := os.Environ()
	env = append(env,
		fmt.Sprintf("WINEPREFIX=%s", prefixDir),
		"WINEDEBUG=-all,err+module",
	)

	// Point winetricks to the Proton/custom wine binary if available
	wineBin := GetBuildWineBinary(protonBuildPath)
	if wineBin != "" {
		env = append(env, fmt.Sprintf("WINE=%s", wineBin))
		wineserverPath := filepath.Join(filepath.Dir(wineBin), "wineserver")
		if _, err := os.Stat(wineserverPath); err == nil {
			env = append(env, fmt.Sprintf("WINESERVER=%s", wineserverPath))
		}
	}

	// Install dependencies: corefonts, vcrun6, mfc42
	deps := []string{"corefonts", "vcrun6", "mfc42"}
	for _, dep := range deps {
		emit(fmt.Sprintf("Installing %s...", dep))

		cmd := exec.Command(winetricksPath, "-q", dep)
		cmd.Env = env

		output, err := cmd.CombinedOutput()
		if len(output) > 0 {
			emit(string(output))
		}
		if err != nil {
			emit(fmt.Sprintf("Warning: failed to install %s: %v", dep, err))
			// Continue with other deps
		} else {
			emit(fmt.Sprintf("%s installed successfully", dep))
		}
	}

	// Mark deps as installed
	marker := filepath.Join(pm.PrefixPath, ".neocron-deps-installed")
	os.WriteFile(marker, []byte("corefonts vcrun6 mfc42\n"), 0644)

	emit("All dependencies installed")
	return nil
}

// downloadWinetricks downloads winetricks to a local cache.
func (pm *PrefixManager) downloadWinetricks() (string, error) {
	cacheDir := filepath.Join(pm.PrefixPath, ".cache")
	os.MkdirAll(cacheDir, 0755)
	dest := filepath.Join(cacheDir, "winetricks")

	cmd := exec.Command("curl", "-sL", "-o", dest,
		"https://raw.githubusercontent.com/Winetricks/winetricks/master/src/winetricks")
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("download winetricks: %w", err)
	}

	os.Chmod(dest, 0755)
	return dest, nil
}

// buildProtonEnv constructs the environment variables for running a game via Proton.
// extraOverrides is a list of DLL basenames (e.g. "d3d8", "dxgi") to set to
// native,builtin in addition to the baseline quartz override.
func (pm *PrefixManager) buildProtonEnv(protonBuildPath string, extraOverrides []string) []string {
	env := os.Environ()

	var filtered []string
	for _, e := range env {
		key := strings.SplitN(e, "=", 2)[0]
		switch key {
		case "STEAM_COMPAT_DATA_PATH", "STEAM_COMPAT_CLIENT_INSTALL_PATH",
			"WINEPREFIX", "WINEDEBUG", "WINEDLLOVERRIDES":
			continue
		default:
			filtered = append(filtered, e)
		}
	}

	filtered = append(filtered,
		fmt.Sprintf("STEAM_COMPAT_DATA_PATH=%s", pm.PrefixPath),
		fmt.Sprintf("STEAM_COMPAT_CLIENT_INSTALL_PATH=%s", protonBuildPath),
		"WINEDEBUG=-all,err+module",
		ComposeDLLOverrides(extraOverrides),
	)

	return filtered
}

// BuildGameEnv returns the environment variables needed to launch a game through Proton.
func (pm *PrefixManager) BuildGameEnv(protonBuildPath string, opts LaunchEnvOpts) []string {
	env := pm.buildProtonEnv(protonBuildPath, opts.ExtraDLLOverrides)

	if opts.EnableDXVK {
		env = append(env, "PROTON_USE_WINED3D=0")
	} else {
		env = append(env, "PROTON_USE_WINED3D=1")
	}

	if opts.EnableMangoHud {
		env = append(env, "MANGOHUD=1")
	}

	// Addon-supplied env vars (e.g. ENABLE_VKBASALT=1, VKBASALT_CONFIG_FILE=...).
	// Last-write wins, so addon vars override anything inherited from os.Environ().
	for k, v := range opts.ExtraEnv {
		env = append(env, k+"="+v)
	}

	return env
}

// LaunchEnvOpts configures the game launch environment.
type LaunchEnvOpts struct {
	EnableDXVK     bool
	EnableMangoHud bool
	// ExtraDLLOverrides are DLL basenames (no extension, lowercase) that should
	// be set to native,builtin. The baseline "quartz" override is always added.
	ExtraDLLOverrides []string
	// ExtraEnv are additional KEY=VALUE-style entries appended to the
	// process environment. Keys already present in os.Environ() are
	// overwritten by entries here. Sourced from enabled addons'
	// AddonManifest.EnvVars at launch time.
	ExtraEnv map[string]string
}

// ComposeDLLOverrides builds a WINEDLLOVERRIDES env-var string of the form
// "WINEDLLOVERRIDES=quartz=n,b;dll1=n,b;dll2=n,b". The baseline "quartz"
// override (Neocron requires native quartz for video playback) is always
// included; extra basenames are appended de-duplicated and lowercased.
func ComposeDLLOverrides(extra []string) string {
	seen := map[string]bool{"quartz": true}
	parts := []string{"quartz=n,b"}
	for _, dll := range extra {
		key := strings.ToLower(strings.TrimSpace(dll))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		parts = append(parts, key+"=n,b")
	}
	return "WINEDLLOVERRIDES=" + strings.Join(parts, ";")
}

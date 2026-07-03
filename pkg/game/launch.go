// Package game starts and monitors the Neocron Classic client. The launch
// command line is reproduced from the official launcher (docs/RE_LAUNCHER.md
// §5.3):
//
//	neocron.exe -ticketuser "<accountName>" -ticket <ticketValue>
//
// On Windows the exe runs natively; on Linux it runs under Proton or Wine,
// reusing the shared pkg/proton runtime management.
package game

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"nc1launcher/pkg/config"
	"nc1launcher/pkg/proton"
)

// Status describes the running state of the game process.
type Status struct {
	Running  bool   `json:"running"`
	PID      int    `json:"pid"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

// LaunchOpts are the per-launch inputs from the auth/account flow.
type LaunchOpts struct {
	AccountName string // -ticketuser
	Ticket      string // -ticket
}

// Launcher owns the child game process.
type Launcher struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	status Status
}

// NewLauncher returns an idle Launcher.
func NewLauncher() *Launcher { return &Launcher{} }

// Status returns the current process status (copy).
func (l *Launcher) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// IsRunning reports whether the game child process is live.
func (l *Launcher) IsRunning() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status.Running
}

// gameArgs builds the ticket arguments exactly as the official launcher does.
func gameArgs(o LaunchOpts) []string {
	// Note: exec.Command passes each arg without a shell, so the account name
	// is a single argv element — no manual quoting needed (Windows argv join
	// re-quotes it). The official cmdline string is: -ticketuser "<name>" -ticket <t>
	return []string{"-ticketuser", o.AccountName, "-ticket", o.Ticket}
}

// Launch starts the client. onOutput receives stdout/stderr lines; onExit fires
// once when the process ends. Returns an error if a launch cannot be started.
func (l *Launcher) Launch(cfg *config.Config, o LaunchOpts, onOutput func(string), onExit func(Status)) error {
	l.mu.Lock()
	if l.status.Running {
		l.mu.Unlock()
		return fmt.Errorf("game is already running")
	}
	l.mu.Unlock()

	if o.Ticket == "" {
		return fmt.Errorf("no launch ticket")
	}
	exePath := cfg.ResolveGameExe()
	if _, err := os.Stat(exePath); err != nil {
		return fmt.Errorf("game not installed at %s", exePath)
	}
	args := gameArgs(o)

	cmd, env, err := buildCommand(cfg, exePath, args)
	if err != nil {
		return err
	}
	// Run from the per-channel client dir — Neocron.exe resolves data.pak, data/
	// and ini/ relative to its working directory.
	cmd.Dir = cfg.ClientDir()
	cmd.Env = env

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start game: %w", err)
	}

	l.mu.Lock()
	l.cmd = cmd
	l.status = Status{Running: true, PID: cmd.Process.Pid}
	l.mu.Unlock()

	if onOutput != nil {
		go pump(stdout, onOutput)
		go pump(stderr, onOutput)
	}

	go func() {
		err := cmd.Wait()
		st := Status{Running: false}
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				st.ExitCode = ee.ExitCode()
			} else {
				st.Error = err.Error()
			}
		}
		l.mu.Lock()
		l.status = st
		l.cmd = nil
		l.mu.Unlock()
		if onExit != nil {
			onExit(st)
		}
	}()
	return nil
}

// Kill terminates the running game process, if any.
func (l *Launcher) Kill() error {
	l.mu.Lock()
	cmd := l.cmd
	l.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// buildCommand assembles the exec.Cmd + environment for the active runtime.
func buildCommand(cfg *config.Config, exePath string, args []string) (*exec.Cmd, []string, error) {
	if runtime.GOOS == "windows" || cfg.RuntimeMode == "native" {
		return exec.Command(exePath, args...), append(os.Environ(), cfg.ExtraEnv...), nil
	}

	switch cfg.RuntimeMode {
	case "proton":
		if cfg.ProtonPath == "" {
			return nil, nil, fmt.Errorf("no Proton build selected — configure one in Settings")
		}
		prefixMgr := proton.NewPrefixManager(cfg.PrefixPath)
		env := prefixMgr.BuildGameEnv(cfg.ProtonPath, proton.LaunchEnvOpts{
			EnableDXVK:     cfg.EnableDXVK,
			EnableMangoHud: cfg.EnableMangoHud,
		})
		env = append(env, cfg.ExtraEnv...)
		if script := proton.GetProtonScript(cfg.ProtonPath); script != "" {
			return exec.Command("python3", append([]string{script, "run", exePath}, args...)...), env, nil
		}
		wineBin := proton.GetBuildWineBinary(cfg.ProtonPath)
		if wineBin == "" {
			return nil, nil, fmt.Errorf("no proton script or wine binary in %s", cfg.ProtonPath)
		}
		return exec.Command(wineBin, append([]string{exePath}, args...)...), env, nil

	default: // "wine"
		wineBin, err := exec.LookPath("wine")
		if err != nil {
			return nil, nil, fmt.Errorf("wine not found in PATH")
		}
		env := append(os.Environ(), "WINEDEBUG=-all,err+module")
		env = append(env, cfg.ExtraEnv...)
		return exec.Command(wineBin, append([]string{exePath}, args...)...), env, nil
	}
}

func pump(r io.Reader, onLine func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		onLine(sc.Text())
	}
}

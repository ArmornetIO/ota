package restart

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/ArmornetIO/ota/internal/stage"
	"github.com/ArmornetIO/ota/internal/swap"
)

// Supervise modes for the launcher.
const (
	// SuperviseAuto restarts crashed children unless a service manager is
	// already supervising the launcher, in which case crashes are its job.
	SuperviseAuto   = "auto"
	SuperviseAlways = "always"
	SuperviseNever  = "never"
)

// envAdopt carries a still-running child's pid across the launcher's re-exec
// into a new version of itself; envReexecFrom stops a re-exec loop.
const (
	envAdopt      = "OTA_LAUNCHER_ADOPT"
	envReexecFrom = "OTA_LAUNCHER_REEXEC_FROM"
)

// LauncherOptions configures the resident launcher.
type LauncherOptions struct {
	// Binary is the installed program; the child is Binary run with Args.
	Binary   string
	StateDir string
	Args     []string
	// Version is the launcher's own version, compiled into the same binary.
	Version string

	Supervise   string        // default auto
	MaxCrashes  int           // default 5 within CrashWindow
	CrashWindow time.Duration // default 10m
	BackoffMin  time.Duration // default 1s
	BackoffMax  time.Duration // default 60s
	StopTimeout time.Duration // default 30s before a stopped child is killed
	Poll        time.Duration // default 250ms

	Stdout, Stderr io.Writer
	Logger         *slog.Logger
}

// ErrGaveUp reports that the child crashed MaxCrashes times within the window.
var ErrGaveUp = errors.New("restart: child keeps crashing; launcher gave up")

// RunLauncher runs the program as a child of this process and keeps it
// running across updates:
//
//   - a one-shot update's restart request restarts the child on the new binary;
//   - a child exiting with ExitRelaunch (it updated or rolled back itself) is
//     restarted at once;
//   - a crashed child is restarted with backoff when supervising, and the
//     launcher gives up after MaxCrashes in CrashWindow;
//   - once a new version commits, the launcher re-executes itself onto it and
//     adopts the running child, so both run the new code with no restart.
//
// Rollback of a version that never becomes healthy happens in the child's own
// Boot; the launcher only has to keep starting it. It returns the exit code the
// launcher should exit with.
func RunLauncher(ctx context.Context, o LauncherOptions) (int, error) {
	o = launcherDefaults(o)
	lock, err := stage.AcquireNamed(o.StateDir, stage.LauncherLock)
	if err != nil {
		return 1, fmt.Errorf("restart: launcher lock: %w", err)
	}
	defer lock.Release()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	supervise := o.Supervise == SuperviseAlways || (o.Supervise == SuperviseAuto && !externallySupervised())
	layout := swap.ForState(o.Binary, o.StateDir)
	reqPath := filepath.Join(o.StateDir, RestartRequest)

	ch, err := adoptOrStart(o)
	if err != nil {
		return 1, err
	}
	var crashes []time.Time
	backoff := o.BackoffMin
	tick := time.NewTicker(o.Poll)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return ch.stop(o.StopTimeout), nil

		case s := <-sigs:
			o.Logger.Info("ota launcher: stopping", "signal", s.String())
			return ch.stop(o.StopTimeout), nil

		case <-tick.C:
			if _, err := os.Stat(reqPath); err == nil {
				_ = stage.Remove(reqPath)
				o.Logger.Info("ota launcher: restart requested by update")
				ch.stop(o.StopTimeout)
				if ch, err = startChild(o); err != nil {
					return 1, err
				}
				continue
			}
			if reexecDue(layout, o.Version) {
				o.Logger.Info("ota launcher: moving onto the committed version")
				if err := reexecSelf(o.Binary, ch.pid(), o.Version); err != nil {
					o.Logger.Warn("ota launcher: re-exec failed; continuing on the old version", "err", err)
				}
			}

		case code := <-ch.done:
			switch {
			case code == ExitRelaunch:
				o.Logger.Info("ota launcher: child asked to be relaunched")
			case code == 0:
				return 0, nil
			case !supervise:
				return code, nil
			default:
				now := time.Now()
				crashes = append(crashes, now)
				for len(crashes) > 0 && now.Sub(crashes[0]) > o.CrashWindow {
					crashes = crashes[1:]
				}
				if len(crashes) >= o.MaxCrashes {
					return code, fmt.Errorf("%w (%d crashes in %s, last exit %d)", ErrGaveUp, len(crashes), o.CrashWindow, code)
				}
				o.Logger.Warn("ota launcher: child crashed; restarting", "exit", code, "backoff", backoff)
				select {
				case <-time.After(backoff):
				case s := <-sigs:
					o.Logger.Info("ota launcher: stopping", "signal", s.String())
					return code, nil
				}
				backoff = min(backoff*2, o.BackoffMax)
			}
			if ch, err = startChild(o); err != nil {
				return 1, err
			}
		}
	}
}

func launcherDefaults(o LauncherOptions) LauncherOptions {
	if o.Supervise == "" {
		o.Supervise = SuperviseAuto
	}
	if o.MaxCrashes == 0 {
		o.MaxCrashes = 5
	}
	if o.CrashWindow == 0 {
		o.CrashWindow = 10 * time.Minute
	}
	if o.BackoffMin == 0 {
		o.BackoffMin = time.Second
	}
	if o.BackoffMax == 0 {
		o.BackoffMax = time.Minute
	}
	if o.StopTimeout == 0 {
		o.StopTimeout = 30 * time.Second
	}
	if o.Poll == 0 {
		o.Poll = 250 * time.Millisecond
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// externallySupervised reports whether a service manager runs this process,
// from the variables systemd and launchd set for their services.
func externallySupervised() bool {
	if os.Getenv("INVOCATION_ID") != "" {
		return true
	}
	if v := os.Getenv("XPC_SERVICE_NAME"); v != "" && v != "0" {
		return true
	}
	return false
}

// reexecDue reports whether a new version has committed since this launcher
// started, so the launcher itself is now the old code.
func reexecDue(l swap.Layout, version string) bool {
	if os.Getenv(envReexecFrom) == version {
		// Already re-executed once and still mismatched: the state file does
		// not describe this binary, and re-executing again would loop.
		return false
	}
	if p, err := swap.LoadPending(l); err != nil || p != nil {
		return false
	}
	st, err := swap.LoadState(l)
	return err == nil && st.RunningVersion != "" && st.RunningVersion != version
}

// childProc is a running child, started here or adopted across a re-exec.
type childProc struct {
	proc *os.Process
	done chan int
}

func (c *childProc) pid() int { return c.proc.Pid }

// stop asks the child to exit, kills it after timeout, and returns its code.
func (c *childProc) stop(timeout time.Duration) int {
	_ = interrupt(c.proc)
	select {
	case code := <-c.done:
		return code
	case <-time.After(timeout):
		_ = c.proc.Kill()
		return <-c.done
	}
}

func startChild(o LauncherOptions) (*childProc, error) {
	// #nosec G204 -- the program's own install path and its own arguments
	cmd := exec.Command(o.Binary, o.Args...) //nolint:gosec
	cmd.Env = append(os.Environ(), EnvLauncher+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, o.Stdout, o.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("restart: start child: %w", err)
	}
	c := &childProc{proc: cmd.Process, done: make(chan int, 1)}
	go func() {
		err := cmd.Wait()
		c.done <- exitCode(cmd.ProcessState, err)
	}()
	return c, nil
}

func adoptOrStart(o LauncherOptions) (*childProc, error) {
	raw := os.Getenv(envAdopt)
	_ = os.Unsetenv(envAdopt)
	if raw == "" {
		return startChild(o)
	}
	pid, err := strconv.Atoi(raw)
	if err != nil {
		return startChild(o)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return startChild(o)
	}
	c := &childProc{proc: p, done: make(chan int, 1)}
	go func() {
		st, err := p.Wait()
		c.done <- exitCode(st, err)
	}()
	return c, nil
}

func exitCode(st *os.ProcessState, err error) int {
	if st != nil {
		if code := st.ExitCode(); code >= 0 {
			return code
		}
		return 128 + int(signalOf(st))
	}
	if err != nil {
		return 1
	}
	return 0
}

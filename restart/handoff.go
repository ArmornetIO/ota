package restart

import (
	"context"
	"path/filepath"
	"time"

	"github.com/ArmornetIO/ota/internal/stage"
	"github.com/ArmornetIO/ota/lifecycle"
	"github.com/ArmornetIO/ota/service"
)

// RestartRequest is the file a one-shot update drops for the launcher. The
// state directory is owner-only, so only the program's own user can ask.
const RestartRequest = "restart.request"

// Hand-off paths, in the order they are tried.
const (
	PathLauncher = "launcher"
	PathService  = "service"
	PathStaged   = "staged"
)

// HandOff is the strategy for a one-shot `update` run from outside the
// program: it never restarts itself. After the swap it asks a running launcher
// to restart its child; failing that, it restarts a registered service;
// failing that, it leaves the new version to start next time. Path reports
// which one happened.
type HandOff struct {
	// StateDir defaults to the plan's.
	StateDir string
	// Service, if set, is the service to restart when no launcher is running.
	Service *service.Spec
	// Backend defaults to the machine's service manager.
	Backend service.Backend

	path string
}

// Path returns which hand-off ran, after the chain has.
func (h *HandOff) Path() string { return h.path }

// Chain implements Restarter. Path is only meaningful after it has run.
func (h *HandOff) Chain(p Plan) []lifecycle.Hook {
	return []lifecycle.Hook{
		{
			Name:    StepSwap,
			OnStart: func(context.Context) error { return p.Install() },
			OnStop:  func(context.Context) error { return p.Restore() },
		},
		{
			Name: StepRestart,
			OnStart: func(context.Context) error {
				if p.BeforeRestart != nil {
					p.BeforeRestart()
				}
				return h.handOff(p)
			},
		},
	}
}

func (h *HandOff) handOff(p Plan) error {
	dir := h.StateDir
	if dir == "" {
		dir = p.StateDir
	}
	if stage.Held(dir, stage.LauncherLock) {
		h.path = PathLauncher
		return stage.WriteJSON(filepath.Join(dir, RestartRequest), map[string]any{
			"version":      p.Version,
			"requested_at": time.Now().UnixMilli(),
		})
	}
	if h.Service != nil {
		b := h.Backend
		if b == nil {
			b = service.OS()
		}
		if st, err := b.Query(*h.Service); err == nil && st.Installed {
			h.path = PathService
			return b.Restart(*h.Service)
		}
	}
	h.path = PathStaged
	if p.DeferGate != nil {
		return p.DeferGate()
	}
	return nil
}

// Relaunch is never reached: a one-shot update runs no health gate itself.
func (h *HandOff) Relaunch(string) error { return nil }

package restart

import (
	"context"

	"github.com/ArmornetIO/ota/lifecycle"
	"github.com/ArmornetIO/ota/service"
)

// ServiceInPlace swaps the binary under a registered service and restarts the
// service, which starts the new file. The new process's Boot runs the health
// gate; a rollback restores the old binary and restarts the service again.
// A nil backend means the machine's service manager.
func ServiceInPlace(spec service.Spec, b service.Backend) Restarter {
	if b == nil {
		b = service.OS()
	}
	return serviceInPlace{spec: spec, b: b}
}

type serviceInPlace struct {
	spec service.Spec
	b    service.Backend
}

func (s serviceInPlace) Chain(p Plan) []lifecycle.Hook {
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
				// Called from inside the service, this stops the calling process
				// part way through; the swap and pending record are already on
				// disk, so the new process's Boot carries on from there.
				return s.b.Restart(s.spec)
			},
		},
	}
}

func (s serviceInPlace) Relaunch(string) error { return s.b.Restart(s.spec) }

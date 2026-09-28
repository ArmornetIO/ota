package restart

import (
	"context"
	"os"

	"github.com/ArmornetIO/ota/lifecycle"
)

// SelfExec replaces the running process with the new binary, keeping its
// arguments and environment. The new process's Boot runs the health gate.
func SelfExec() Restarter { return selfExec{} }

type selfExec struct{}

func (selfExec) Chain(p Plan) []lifecycle.Hook {
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
				return execSelf(p.Binary, os.Args, os.Environ())
			},
		},
	}
}

func (selfExec) Relaunch(binary string) error { return execSelf(binary, os.Args, os.Environ()) }

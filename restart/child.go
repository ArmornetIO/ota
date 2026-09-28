package restart

import (
	"context"
	"os"

	"github.com/ArmornetIO/ota/lifecycle"
)

// EnvLauncher is set in the environment of a program started by the resident
// launcher, so the program knows restarting means exiting back to it.
const EnvLauncher = "OTA_LAUNCHER"

// ExitRelaunch is the exit code a program under the launcher uses to ask for
// an immediate restart. The launcher does not count it as a crash.
const ExitRelaunch = 75

// UnderLauncher reports whether this process was started by the launcher.
func UnderLauncher() bool { return os.Getenv(EnvLauncher) != "" }

// Child is the strategy for a program run by the launcher: after the swap it
// exits with ExitRelaunch and the launcher starts the new binary at once.
// client.New picks it automatically under a launcher.
func Child() Restarter { return child{} }

type child struct{}

func (child) Chain(p Plan) []lifecycle.Hook {
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
				os.Exit(ExitRelaunch)
				return nil
			},
		},
	}
}

func (child) Relaunch(string) error {
	os.Exit(ExitRelaunch)
	return nil
}

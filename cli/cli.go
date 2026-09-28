// Package cli gives a host program ota's commands as subcommands of its own
// binary, so updating never needs a second program:
//
//	switch os.Args[1] {
//	case "update", "run", "offer":
//		os.Exit(cli.Commands(config)[os.Args[1]](os.Args[2:]))
//	}
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/client"
	"github.com/ArmornetIO/ota/restart"
	"github.com/ArmornetIO/ota/service"
)

// Exit codes. Stable, so schedulers and scripts can branch on them.
const (
	ExitOK            = 0
	ExitError         = 1
	ExitDeferred      = 10
	ExitDrainTimeout  = 11
	ExitRefused       = 20
	ExitDownload      = 30
	ExitRolledBack    = 40
	ExitLocked        = 50
	ExitRestartNeeded = 60
)

// Config is what the host supplies. It is a function so the host loads its
// configuration only when a command actually runs.
type Config struct {
	// Client configures the updater for this program. Restarter is ignored by
	// `update`, which always hands off (see restart.HandOff).
	Client client.Options
	// Service, if set, is the service `update` restarts when no launcher runs.
	Service *service.Spec
	// Launcher tunes `run`. Binary, StateDir and Version come from Client.
	Launcher restart.LauncherOptions
}

// Commands returns the subcommands, keyed by name. Each returns an exit code.
func Commands(cfg func() (Config, error)) map[string]func(args []string) int {
	return map[string]func([]string) int{
		"update": func(args []string) int { return runUpdate(cfg, args) },
		"run":    func(args []string) int { return runLauncher(cfg, args) },
		"offer":  runOffer,
	}
}

// ExitCode maps an ota error to its exit code.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ota.ErrLocked):
		return ExitLocked
	case errors.Is(err, ota.ErrRestartNeeded):
		return ExitRestartNeeded
	case errors.Is(err, ota.ErrDrainTimeout):
		return ExitDrainTimeout
	case errors.Is(err, ota.ErrDeferred):
		return ExitDeferred
	case errors.Is(err, ota.ErrRefused):
		return ExitRefused
	case errors.Is(err, ota.ErrDownload):
		return ExitDownload
	case errors.Is(err, ota.ErrRolledBack):
		return ExitRolledBack
	}
	return ExitError
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "ota: "+format+"\n", args...)
	return ExitError
}

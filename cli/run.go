package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ArmornetIO/ota/client"
	"github.com/ArmornetIO/ota/restart"
)

// runLauncher is `run [flags] -- <args>`: it runs the program itself as a
// child, from the same binary, and keeps it running across updates.
func runLauncher(cfg func() (Config, error), args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	supervise := fs.String("supervise", restart.SuperviseAuto, "restart crashed children: auto, always or never")
	maxCrashes := fs.Int("max-crashes", 0, "give up after this many crashes within -crash-window (default 5)")
	crashWindow := fs.Duration("crash-window", 0, "window for -max-crashes (default 10m)")
	backoffMin := fs.Duration("backoff-min", 0, "first restart delay after a crash (default 1s)")
	backoffMax := fs.Duration("backoff-max", 0, "longest restart delay (default 60s)")
	stopTimeout := fs.Duration("stop-timeout", 0, "how long a stopping child gets before it is killed (default 30s)")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}
	switch *supervise {
	case restart.SuperviseAuto, restart.SuperviseAlways, restart.SuperviseNever:
	default:
		return fail("-supervise must be auto, always or never")
	}

	conf, err := cfg()
	if err != nil {
		return fail("config: %v", err)
	}
	c, err := client.New(conf.Client)
	if err != nil {
		return fail("%v", err)
	}
	lo := conf.Launcher
	lo.Binary, lo.StateDir, lo.Version = c.BinaryPath(), c.StateDir(), conf.Client.CurrentVersion
	lo.Args = fs.Args()
	lo.Supervise = *supervise
	// Flags override the host's configured values only when given.
	if *maxCrashes != 0 {
		lo.MaxCrashes = *maxCrashes
	}
	if *crashWindow != 0 {
		lo.CrashWindow = *crashWindow
	}
	if *backoffMin != 0 {
		lo.BackoffMin = *backoffMin
	}
	if *backoffMax != 0 {
		lo.BackoffMax = *backoffMax
	}
	if *stopTimeout != 0 {
		lo.StopTimeout = *stopTimeout
	}
	if len(lo.Args) == 0 {
		return fail("run: give the program's arguments after --, e.g. run -- serve")
	}

	code, err := restart.RunLauncher(context.Background(), lo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ota: %v\n", err)
	}
	return code
}

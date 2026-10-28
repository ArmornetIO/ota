package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/client"
	"github.com/ArmornetIO/ota/restart"
)

type updateOutput struct {
	Result  string `json:"result"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Exit    int    `json:"exit"`
}

// runUpdate is the one-shot `update`. It never restarts itself: after the swap
// it hands the new version to whatever runs the program and, when something
// does, waits for the new version's verdict.
func runUpdate(cfg func() (Config, error), args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "check and verify the offer; download nothing")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	wait := fs.Duration("wait", 0, "how long to wait for the new version's verdict (default: commit deadline + 30s)")
	// A stalled server would otherwise hold the update lock until killed, and
	// every later run would exit 50.
	limit := fs.Duration("timeout", time.Hour, "give up on the check and download after this long")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}
	conf, err := cfg()
	if err != nil {
		return fail("config: %v", err)
	}
	h := &restart.HandOff{Service: conf.Service}
	opts := conf.Client
	opts.Restarter = h
	c, err := client.New(opts)
	if err != nil {
		return fail("%v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	uctx, cancel := context.WithTimeout(ctx, *limit)
	defer cancel()

	out := updateOutput{}
	finish := func(r client.Result, err error) int {
		out.Exit = ExitCode(err)
		if out.Result == "" {
			out.Result = string(r.Stage)
		}
		if out.Version == "" {
			out.Version = r.Version
		}
		if r.Reason != "" {
			out.Reason = r.Reason
		} else if err != nil {
			out.Reason = err.Error()
		}
		printResult(out, *asJSON)
		return out.Exit
	}

	if *dryRun {
		r, err := c.Available(uctx)
		if r.UpToDate {
			out.Result = "uptodate"
		}
		return finish(r, err)
	}

	r, err := c.CheckAndUpdate(uctx)
	if err != nil {
		return finish(r, err)
	}
	if r.UpToDate {
		out.Result = "uptodate"
		return finish(r, nil)
	}
	out.Path = h.Path()
	if h.Path() == restart.PathStaged {
		out.Result = "staged"
		return finish(r, ota.ErrRestartNeeded)
	}
	timeout := *wait
	if timeout == 0 {
		timeout = c.CommitDeadline() + 30*time.Second
	}
	vr, err := c.WaitOutcome(ctx, r.Version, timeout)
	return finish(vr, err)
}

func printResult(o updateOutput, asJSON bool) {
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(o)
		return
	}
	switch o.Result {
	case "uptodate":
		fmt.Printf("up to date (%s)\n", o.Version)
	case "staged":
		fmt.Printf("%s installed; it runs from the next start of the program\n", o.Version)
	case string(ota.StageCommitted):
		fmt.Printf("updated to %s (via %s)\n", o.Version, o.Path)
	case string(ota.StageOfferSeen):
		fmt.Printf("%s is available and verifies\n", o.Version)
	default:
		fmt.Fprintf(os.Stderr, "%s %s: %s\n", o.Result, o.Version, o.Reason)
	}
}

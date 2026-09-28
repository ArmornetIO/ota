package client

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/internal/swap"
)

// Available checks for an offer and verifies it without downloading or
// changing anything. It is the `update --dry-run` path.
func (c *Client) Available(ctx context.Context) (Result, error) {
	st, err := swap.LoadState(c.layout)
	if err != nil {
		return Result{}, err
	}
	so, err := c.opts.Transport.Check(ctx, ota.CheckRequest{
		ClientID: c.opts.ClientID, Program: c.opts.Program, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Channel: c.opts.Channel, RunningVersion: c.current.String(), LastOfferIssuedAt: st.LastOfferIssuedAt,
	})
	if err != nil || so == nil {
		return Result{UpToDate: so == nil && err == nil, Version: c.current.String()}, err
	}
	o, err := c.verify(ctx, *so)
	if err != nil {
		return Result{Stage: ota.StageRefused, Reason: err.Error()}, err
	}
	v, err := c.acceptance(st).accept(o)
	if err != nil {
		return Result{Stage: ota.StageRefused, Version: o.Version, Reason: err.Error()}, err
	}
	if v.upToDate {
		return Result{UpToDate: true, Version: c.current.String()}, nil
	}
	return Result{Stage: ota.StageOfferSeen, Version: o.Version}, nil
}

func (c *Client) acceptance(st swap.State) acceptance {
	return acceptance{
		program: c.opts.Program, channel: c.opts.Channel, goos: runtime.GOOS, goarch: runtime.GOARCH,
		running: c.current, lastIssue: st.LastOfferIssuedAt, now: c.opts.Now(), tolerance: c.opts.ClockTolerance,
	}
}

// WaitOutcome waits for an installed version to commit or roll back, for a
// caller that handed the restart to something else (a launcher, a service
// manager) and needs the verdict. It watches the same state the new process's
// health gate writes, so it works whoever runs the gate.
func (c *Client) WaitOutcome(ctx context.Context, version string, timeout time.Duration) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		p, err := swap.LoadPending(c.layout)
		if err != nil {
			return Result{}, err
		}
		if p == nil {
			st, err := swap.LoadState(c.layout)
			if err != nil {
				return Result{}, err
			}
			if st.RunningVersion == version {
				return Result{Stage: ota.StageCommitted, Version: version}, nil
			}
			err = fmt.Errorf("%w: now running %s", ota.ErrRolledBack, st.RunningVersion)
			return Result{Stage: ota.StageRolledBack, Version: version, Reason: err.Error()}, err
		}
		select {
		case <-ctx.Done():
			return Result{Stage: ota.StageInstalled, Version: version, Reason: "no verdict before timeout"},
				fmt.Errorf("client: version %s has not committed or rolled back after %s", version, timeout)
		case <-t.C:
		}
	}
}

// StateDir is where this client keeps its state, for a launcher or CLI that
// coordinates with it.
func (c *Client) StateDir() string { return c.layout.StateDir }

// BinaryPath is the installed binary this client updates.
func (c *Client) BinaryPath() string { return c.layout.Binary }

// CommitDeadline is how long a new version has to prove itself.
func (c *Client) CommitDeadline() time.Duration { return c.opts.CommitDeadline }

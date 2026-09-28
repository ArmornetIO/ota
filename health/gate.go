package health

import (
	"context"
	"errors"
	"time"
)

// Outcome is the gate's verdict on a new version.
type Outcome struct {
	Commit bool
	Reason string
}

// Gate decides whether a freshly started version is kept.
//
// It commits once the source has reported OK continuously for SteadyWindow, or
// as soon as the program calls Commit. It rolls back on FatalError, or when the
// deadline passes without either. Anything else (Starting, RecoverableError,
// PermanentError, Stopping) keeps it waiting and resets the steady window,
// because a single good sample between bad ones is not evidence of health.
type Gate struct {
	Source       Source
	SteadyWindow time.Duration
	Deadline     time.Time
	// Poll is how often Source is asked. Defaults to one second.
	Poll time.Duration
	// Committed is closed by the program's explicit commit.
	Committed <-chan struct{}
	// Now is for tests.
	Now func() time.Time
}

// Run blocks until the gate decides or ctx ends; a cancelled context is a
// rollback, since nothing proved the version healthy.
func (g Gate) Run(ctx context.Context) Outcome {
	now := g.Now
	if now == nil {
		now = time.Now
	}
	poll := g.Poll
	if poll <= 0 {
		poll = time.Second
	}
	t := time.NewTicker(poll)
	defer t.Stop()

	var steadySince time.Time
	check := func() (Outcome, bool) {
		if !now().Before(g.Deadline) {
			return Outcome{Reason: "not healthy before the commit deadline"}, true
		}
		st, err := g.Source.Status(ctx)
		switch {
		case errors.Is(err, ErrCommitOnly):
			return Outcome{}, false
		case err != nil:
			steadySince = time.Time{}
			return Outcome{}, false
		case st == StatusFatalError:
			return Outcome{Reason: "program reported FatalError"}, true
		case st == StatusOK:
			if steadySince.IsZero() {
				steadySince = now()
			}
			if now().Sub(steadySince) >= g.SteadyWindow {
				return Outcome{Commit: true, Reason: "healthy for the steady window"}, true
			}
		default:
			steadySince = time.Time{}
		}
		return Outcome{}, false
	}

	if o, done := check(); done {
		return o
	}
	for {
		select {
		case <-ctx.Done():
			return Outcome{Reason: "gate cancelled before the version proved healthy"}
		case <-g.Committed:
			return Outcome{Commit: true, Reason: "committed by the program"}
		case <-t.C:
			if o, done := check(); done {
				return o
			}
		}
	}
}

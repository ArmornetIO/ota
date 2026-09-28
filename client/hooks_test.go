package client

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/health"
	"github.com/ArmornetIO/ota/lifecycle"
)

func TestDeferIsPersistedAcrossRuns(t *testing.T) {
	e := newEnv(t)
	e.publish("2.0.0", e.now.Add(-time.Minute), nil)
	asked := 0
	hook := func(context.Context, ota.Offer) ota.Decision {
		asked++
		if asked == 1 {
			return ota.DeferUntil(e.now.Add(time.Hour), "maintenance window")
		}
		return ota.ApplyNow()
	}
	opts := func(o *Options) { o.Hooks.BeforeApply = hook }

	if _, err := e.client("1.0.0", opts).CheckAndUpdate(context.Background()); !errors.Is(err, ota.ErrDeferred) {
		t.Fatalf("first run err = %v, want ErrDeferred", err)
	}
	// A second one-shot run inside the window must not ask again.
	if _, err := e.client("1.0.0", opts).CheckAndUpdate(context.Background()); !errors.Is(err, ota.ErrDeferred) || asked != 1 {
		t.Fatalf("second run err = %v, asked %d times", err, asked)
	}
	if e.installed() != "v1" {
		t.Fatal("swapped during deferral")
	}
	e.now = e.now.Add(2 * time.Hour)
	if _, err := e.client("1.0.0", opts).CheckAndUpdate(context.Background()); err != nil {
		t.Fatalf("run after the window: %v", err)
	}
	if asked != 2 || e.installed() != string(e.newBin) {
		t.Fatalf("not applied after the window (asked %d, installed %q)", asked, e.installed())
	}
}

func TestRefuseReportsAndChangesNothing(t *testing.T) {
	e := newEnv(t)
	e.publish("2.0.0", e.now.Add(-time.Minute), nil)
	c := e.client("1.0.0", func(o *Options) {
		o.Hooks.BeforeApply = func(context.Context, ota.Offer) ota.Decision { return ota.RefuseOffer("pinned by operator") }
	})
	if _, err := c.CheckAndUpdate(context.Background()); !errors.Is(err, ota.ErrDeferred) {
		t.Fatalf("err = %v", err)
	}
	if e.installed() != "v1" || !slices.Contains(e.tr.stages(), ota.StageRefused) {
		t.Fatalf("installed %q, reports %v", e.installed(), e.tr.stages())
	}
}

// A drain that overruns abandons the attempt: nothing is swapped and nothing
// is left on disk.
func TestDrainTimeoutAbandonsWithoutSwapping(t *testing.T) {
	e := newEnv(t)
	e.publish("2.0.0", e.now.Add(-time.Minute), nil)
	c := e.client("1.0.0", func(o *Options) {
		o.Hooks.DrainTimeout = 20 * time.Millisecond
		o.Hooks.Drain = func(ctx context.Context) error {
			<-time.After(time.Second)
			return nil
		}
	})
	r, err := c.CheckAndUpdate(context.Background())
	if !errors.Is(err, ota.ErrDrainTimeout) || r.Stage != ota.StageFailed {
		t.Fatalf("err = %v, stage %s", err, r.Stage)
	}
	if e.installed() != "v1" || e.rs.restarted != 0 {
		t.Fatal("swapped or restarted after a drain timeout")
	}
	// The same offer is still acceptable: the floor did not move.
	c = e.client("1.0.0", nil)
	if _, err := c.CheckAndUpdate(context.Background()); err != nil || e.installed() != string(e.newBin) {
		t.Fatalf("retry after drain timeout: %v, installed %q", err, e.installed())
	}
}

func TestDrainRunsBeforeSwapAndChainIsEditable(t *testing.T) {
	e := newEnv(t)
	e.publish("2.0.0", e.now.Add(-time.Minute), nil)
	var order []string
	c := e.client("1.0.0", func(o *Options) {
		o.Hooks.Drain = func(context.Context) error {
			order = append(order, "drain:"+e.installed())
			return nil
		}
		o.EditChain = func(m *lifecycle.Manager) {
			m.Insert(m.Index("restart"), lifecycle.Hook{Name: "audit", OnStart: func(context.Context) error {
				order = append(order, "audit:"+e.installed())
				return nil
			}})
		}
	})
	if _, err := c.CheckAndUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"drain:v1", "audit:" + string(e.newBin)}
	if !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestAfterApplyAndOnRollback(t *testing.T) {
	for _, tc := range []struct {
		status string
		commit bool
	}{{health.StatusOK, true}, {health.StatusFatalError, false}} {
		e := newEnv(t)
		e.publish("2.0.0", e.now.Add(-time.Minute), nil)
		if _, err := e.client("1.0.0", nil).CheckAndUpdate(context.Background()); err != nil {
			t.Fatal(err)
		}
		var after, rolled bool
		c := e.client("2.0.0", func(o *Options) {
			o.Health = health.StatusFunc(func() string { return tc.status })
			o.CommitDeadline = time.Hour
			o.Hooks.AfterApply = func(context.Context, Result) { after = true }
			o.Hooks.OnRollback = func(context.Context, Result) { rolled = true }
		})
		br, err := c.Boot(context.Background())
		if err != nil || br.Gate == nil {
			t.Fatalf("Boot = %v, gate %v", err, br.Gate)
		}
		r := <-br.Gate
		if tc.commit {
			if r.Stage != ota.StageCommitted || !after || rolled {
				t.Fatalf("commit path: %+v after=%v rolled=%v", r, after, rolled)
			}
			continue
		}
		if r.Stage != ota.StageRolledBack || !rolled || after || e.installed() != "v1" || len(e.rs.relaunched) != 1 {
			t.Fatalf("rollback path: %+v after=%v rolled=%v installed=%q relaunched=%v", r, after, rolled, e.installed(), e.rs.relaunched)
		}
	}
}

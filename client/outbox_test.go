package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/internal/stage"
)

// flakyTransport fails reports until up is set.
type flakyTransport struct {
	*memTransport
	up bool
}

func (f *flakyTransport) Report(ctx context.Context, rs []ota.Report) error {
	if !f.up {
		return errors.New("server unreachable")
	}
	return f.memTransport.Report(ctx, rs)
}

func TestUndeliveredReportsAreKeptAndFlushed(t *testing.T) {
	e := newEnv(t)
	e.publish("2.0.0", e.now.Add(-time.Minute), nil)
	ft := &flakyTransport{memTransport: e.tr}
	c := e.client("1.0.0", func(o *Options) { o.Transport = ft })
	if _, err := c.CheckAndUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var box []ota.Report
	if err := stage.ReadJSON(c.outboxPath(), &box); err != nil || len(box) == 0 {
		t.Fatalf("outbox = %v, %v", box, err)
	}
	if len(e.tr.stages()) != 0 {
		t.Fatal("reports delivered while the server was down")
	}

	ft.up = true
	c2 := e.client("2.0.0", func(o *Options) { o.Transport = ft })
	if _, err := c2.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.tr.stages(); !slices.Contains(got, ota.StageInstalled) || !slices.Contains(got, ota.StageDownloading) {
		t.Fatalf("flushed stages = %v", got)
	}
	if _, err := os.Stat(c.outboxPath()); !os.IsNotExist(err) {
		t.Fatal("outbox kept after a successful flush")
	}
}

func TestOutboxKeepsLatestPerStageAndIsCapped(t *testing.T) {
	e := newEnv(t)
	c := e.client("1.0.0", nil)
	for i := range 100 {
		c.keep([]ota.Report{{Version: fmt.Sprint("1.0.", i), Stage: ota.StageFailed, Error: "x"}})
	}
	c.keep([]ota.Report{{Version: "1.0.99", Stage: ota.StageFailed, Error: "latest"}})
	var box []ota.Report
	_ = stage.ReadJSON(c.outboxPath(), &box)
	if len(box) != outboxCap {
		t.Fatalf("outbox holds %d, want %d", len(box), outboxCap)
	}
	n := 0
	for _, r := range box {
		if r.Version == "1.0.99" {
			n++
			if r.Error != "latest" {
				t.Fatal("older duplicate kept over the latest")
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d entries for one version and stage", n)
	}
}

func TestReporterReplacesTransport(t *testing.T) {
	e := newEnv(t)
	e.publish("2.0.0", e.now.Add(-time.Minute), nil)
	var viaHost []ota.Stage
	c := e.client("1.0.0", func(o *Options) {
		o.Reporter = func(_ context.Context, rs []ota.Report) error {
			for _, r := range rs {
				viaHost = append(viaHost, r.Stage)
			}
			return nil
		}
	})
	if _, err := c.CheckAndUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(viaHost) == 0 || len(e.tr.stages()) != 0 {
		t.Fatalf("host got %v, transport got %v", viaHost, e.tr.stages())
	}
}

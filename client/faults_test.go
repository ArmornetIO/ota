package client_test

import (
	"bytes"
	stded "crypto/ed25519"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ArmornetIO/ota/internal/swap"
	"github.com/ArmornetIO/ota/internal/testkit/fixture"
)

// These tests build sampleapp twice (v1 and v2), install v1, serve v2 from a
// real server, and run the real binaries.

type faults struct {
	*fixture.Fixture
	layout swap.Layout
}

func newFaults(t *testing.T, v2behaviour string, signWith stded.PrivateKey) *faults {
	f := fixture.NewFixture(t, v2behaviour, signWith)
	return &faults{Fixture: f, layout: swap.For(f.Bin)}
}

// run starts the installed binary once and waits for it and anything it
// exec'd into. killAt injects a crash at that step.
func (f *faults) run(killAt string) int {
	f.T.Helper()
	cmd := f.Command()
	if killAt != "" {
		cmd.Env = append(cmd.Env, "OTA_TEST_KILL_AT="+killAt)
	}
	done := make(chan error, 1)
	_ = cmd.Start()
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		f.T.Fatalf("sampleapp hung\n%s", f.Lines())
	}
	return cmd.ProcessState.ExitCode()
}

// settle reruns the program, as a supervisor would, until a run ends with
// nothing pending. It returns the last marker line.
func (f *faults) settle() string {
	f.T.Helper()
	for i := 0; i < 8; i++ {
		if p, _ := swap.LoadPending(f.layout); p == nil && i > 0 {
			break
		}
		f.run("")
	}
	if p, _ := swap.LoadPending(f.layout); p != nil {
		f.T.Fatalf("attempt never resolved: %+v\n%s", p, f.Lines())
	}
	ls := strings.Split(strings.TrimSpace(f.Lines()), "\n")
	return ls[len(ls)-1]
}

func (f *faults) assertClean() {
	f.T.Helper()
	entries, _ := os.ReadDir(f.layout.StateDir)
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".ota-") || n == "prev" || n == "pending.json" || n == "download.json" || strings.HasPrefix(n, "bad-") {
			f.T.Errorf("leftover %s in state dir", n)
		}
	}
}

func TestUpdateCommits(t *testing.T) {
	f := newFaults(t, "ok", nil)
	f.run("")
	out := f.Lines()
	if !bytes.Equal(f.Installed(), f.V2) {
		t.Fatalf("v2 not installed\n%s", out)
	}
	if !strings.Contains(out, "gate committed 2.0.0") {
		t.Fatalf("v2 did not commit\n%s", out)
	}
	f.assertClean()

	// An unbroken event sequence from offer to outcome.
	want := []string{"event offer_seen", "event downloading", "event verified", "event installed", "boot 2.0.0", "event committed"}
	at := 0
	for _, l := range strings.Split(out, "\n") {
		if at < len(want) && strings.HasPrefix(l, want[at]) {
			at++
		}
	}
	if at != len(want) {
		t.Fatalf("event sequence broken at %q\n%s", want[at], out)
	}

	// Running again is a no-op: the committed offer is now the replay floor.
	f.run("")
	if !strings.HasSuffix(strings.TrimSpace(f.Lines()), "update uptodate 2.0.0") {
		t.Fatalf("second run did not report up to date\n%s", f.Lines())
	}
}

func TestUntrustedSignatureRefused(t *testing.T) {
	_, other, _ := stded.GenerateKey(nil)
	f := newFaults(t, "ok", other)
	f.run("")
	if !bytes.Equal(f.Installed(), f.V1) {
		t.Fatal("binary changed despite an untrusted signature")
	}
	if !strings.Contains(f.Lines(), "update refused") {
		t.Fatalf("refusal not reported\n%s", f.Lines())
	}
	f.assertClean()
}

func TestUnhealthyVersionRollsBack(t *testing.T) {
	f := newFaults(t, "unhealthy", nil)
	f.run("")
	out := f.Lines()
	if !bytes.Equal(f.Installed(), f.V1) {
		t.Fatalf("unhealthy v2 left installed\n%s", out)
	}
	if !strings.Contains(out, "boot 2.0.0") || !strings.Contains(out, "event rolled_back") {
		t.Fatalf("expected v2 to start and roll back\n%s", out)
	}
	// The failed offer is now the floor: v1 does not retry it in a loop.
	if !strings.HasSuffix(strings.TrimSpace(out), "update uptodate 1.0.0") {
		t.Fatalf("v1 did not settle after rollback\n%s", out)
	}
	f.assertClean()
}

func TestCrashLoopingVersionRollsBack(t *testing.T) {
	f := newFaults(t, "crash", nil)
	f.run("")
	last := f.settle()
	if !bytes.Equal(f.Installed(), f.V1) {
		t.Fatalf("crash-looping v2 left installed\n%s", f.Lines())
	}
	if last != "update uptodate 1.0.0" {
		t.Fatalf("last line %q\n%s", last, f.Lines())
	}
	f.assertClean()
}

// A crash at any step leaves exactly one complete binary, the
// attempt resolves on the next starts, and no temp files survive.
func TestCrashAtEveryStep(t *testing.T) {
	for _, step := range []string{"after-download", "after-pending", "after-link", "after-swap", "before-restart", "in-gate", "before-commit"} {
		t.Run(step, func(t *testing.T) {
			f := newFaults(t, "ok", nil)
			if code := f.run(step); code != 86 {
				t.Fatalf("kill point %s not reached (exit %d)\n%s", step, code, f.Lines())
			}
			got := f.Installed()
			if !bytes.Equal(got, f.V1) && !bytes.Equal(got, f.V2) {
				t.Fatal("install path holds neither v1 nor v2")
			}
			f.settle()
			// Whatever the crash point, the update eventually lands.
			for i := 0; i < 3 && !bytes.Equal(f.Installed(), f.V2); i++ {
				f.run("")
				f.settle()
			}
			if !bytes.Equal(f.Installed(), f.V2) {
				t.Fatalf("update never completed after crash at %s\n%s", step, f.Lines())
			}
			f.run("")
			f.assertClean()
		})
	}
}

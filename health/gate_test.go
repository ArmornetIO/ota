package health

import (
	"context"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// scripted returns statuses in order and advances the clock by step each call.
func scripted(c *clock, step time.Duration, seq ...string) Source {
	i := 0
	return SourceFunc(func(context.Context) (string, error) {
		s := seq[min(i, len(seq)-1)]
		i++
		c.add(step)
		return s, nil
	})
}

func TestGate(t *testing.T) {
	start := time.Unix(1000, 0)
	cases := []struct {
		name   string
		seq    []string
		commit bool
	}{
		{"steady OK commits", []string{StatusStarting, StatusOK, StatusOK, StatusOK, StatusOK}, true},
		{"fatal rolls back", []string{StatusStarting, StatusFatalError}, false},
		{"flapping never steadies", []string{StatusOK, StatusRecoverableError, StatusOK, StatusRecoverableError}, false},
		{"stuck starting hits deadline", []string{StatusStarting}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := &clock{t: start}
			g := Gate{
				Source:       scripted(clk, 10*time.Second, c.seq...),
				SteadyWindow: 30 * time.Second,
				Deadline:     start.Add(2 * time.Minute),
				Poll:         time.Millisecond,
				Now:          clk.now,
			}
			if got := g.Run(context.Background()); got.Commit != c.commit {
				t.Fatalf("Commit = %v (%s), want %v", got.Commit, got.Reason, c.commit)
			}
		})
	}
}

func TestGateExplicitCommit(t *testing.T) {
	ch := make(chan struct{})
	close(ch)
	g := Gate{Source: CommitOnly(), Deadline: time.Now().Add(time.Minute), Poll: time.Millisecond, Committed: ch}
	if got := g.Run(context.Background()); !got.Commit {
		t.Fatalf("explicit commit ignored: %s", got.Reason)
	}
}

func TestGateCommitOnlyTimesOut(t *testing.T) {
	g := Gate{Source: CommitOnly(), Deadline: time.Now().Add(20 * time.Millisecond), Poll: time.Millisecond}
	if got := g.Run(context.Background()); got.Commit {
		t.Fatal("commit-only gate committed without a commit")
	}
}

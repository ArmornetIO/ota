package client

import (
	"bytes"
	"context"
	stded "crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/lifecycle"
	"github.com/ArmornetIO/ota/restart"
	"github.com/ArmornetIO/ota/verify/ed25519"
)

// memTransport serves one signed offer and its artifact from memory, and
// records reports.
type memTransport struct {
	mu      sync.Mutex
	offer   *ota.SignedOffer
	blob    []byte
	reports []ota.Report
	checks  int
}

func (m *memTransport) Check(context.Context, ota.CheckRequest) (*ota.SignedOffer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checks++
	return m.offer, nil
}

func (m *memTransport) Fetch(_ context.Context, _ ota.Artifact, off int64) (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader(m.blob[off:])), off, nil
}

func (m *memTransport) Report(_ context.Context, rs []ota.Report) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reports = append(m.reports, rs...)
	return nil
}

func (m *memTransport) stages() []ota.Stage {
	m.mu.Lock()
	defer m.mu.Unlock()
	var s []ota.Stage
	for _, r := range m.reports {
		s = append(s, r.Stage)
	}
	return s
}

// recRestarter installs but never execs; Relaunch is recorded.
type recRestarter struct {
	relaunched []string
	restarted  int
}

func (r *recRestarter) Chain(p restart.Plan) []lifecycle.Hook {
	return []lifecycle.Hook{
		{
			Name:    restart.StepSwap,
			OnStart: func(context.Context) error { return p.Install() },
			OnStop:  func(context.Context) error { return p.Restore() },
		},
		{
			Name: restart.StepRestart,
			OnStart: func(context.Context) error {
				if p.BeforeRestart != nil {
					p.BeforeRestart()
				}
				r.restarted++
				return nil
			},
		},
	}
}

func (r *recRestarter) Relaunch(bin string) error {
	r.relaunched = append(r.relaunched, bin)
	return nil
}

type env struct {
	t   *testing.T
	bin string
	key stded.PrivateKey
	pub stded.PublicKey
	tr  *memTransport
	rs  *recRestarter
	// clockMu guards now, which the gate goroutine reads concurrently.
	clockMu sync.Mutex
	now     time.Time
	newBin  []byte
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pub, priv, _ := stded.GenerateKey(nil)
	bin := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, bin: bin, key: priv, pub: pub, tr: &memTransport{}, rs: &recRestarter{}, now: time.Unix(10_000, 0)}
}

// publish signs an offer for version, with the given mutations.
func (e *env) publish(version string, issued time.Time, mod func(*ota.Offer)) {
	e.t.Helper()
	e.newBin = []byte("binary " + version)
	sum := sha256.Sum256(e.newBin)
	o := ota.Offer{
		Schema: ota.OfferSchema, Program: "app", Version: version, IssuedAt: issued.UnixMilli(),
		Artifacts: []ota.Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(e.newBin))}},
	}
	if mod != nil {
		mod(&o)
	}
	b, _ := json.Marshal(o)
	e.tr.mu.Lock()
	e.tr.offer = &ota.SignedOffer{Offer: b, Signature: ed25519.Sign(e.key, b), Kind: ed25519.Kind}
	e.tr.blob = e.newBin
	e.tr.mu.Unlock()
}

func (e *env) client(version string, mod func(*Options)) *Client {
	e.t.Helper()
	v, _ := ed25519.New(e.pub)
	o := Options{
		Program: "app", ClientID: "c1", CurrentVersion: version, BinaryPath: e.bin,
		Transport: e.tr, Verifier: v, Restarter: e.rs,
		// The clock ticks a millisecond per read, so a gate's steady window
		// can elapse without real sleeps while tests still control the date.
		Now: func() time.Time {
			e.clockMu.Lock()
			defer e.clockMu.Unlock()
			e.now = e.now.Add(time.Millisecond)
			return e.now
		},
		HealthPoll: time.Millisecond, SteadyWindow: 5 * time.Millisecond,
	}
	if mod != nil {
		mod(&o)
	}
	c, err := New(o)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) installed() string {
	b, _ := os.ReadFile(e.bin)
	return string(b)
}

package server

import (
	"bytes"
	"context"
	stded "crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/verify/ed25519"
)

type harness struct {
	t     *testing.T
	s     *Server
	store *MemStore
	key   stded.PrivateKey
	src   *fakeSource
}

type fakeSource struct{ rels []SourceRelease }

func (f *fakeSource) Name() string { return "fake" }
func (f *fakeSource) Releases(context.Context) ([]SourceRelease, error) {
	return f.rels, nil
}

func asset(name string, b []byte) Asset {
	return Asset{Name: name, Size: int64(len(b)), Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(b)), nil
	}}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pub, priv, _ := stded.GenerateKey(nil)
	v, _ := ed25519.New(pub)
	h := &harness{t: t, store: NewMemStore(), key: priv, src: &fakeSource{}}
	s, err := New(Options{Store: h.store, Blobs: FSBlobStore{Dir: t.TempDir()}, Verifier: v, Source: h.src,
		AuthorizeAdmin: func(r *http.Request) error {
			if r.Header.Get("Authorization") != "Bearer admin" {
				return errors.New("no")
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
	return h
}

// release publishes a signed release with one linux/amd64 binary to the fake
// source and returns the offer bytes.
func (h *harness) release(version string, issued int64, mod func(*ota.Offer)) []byte {
	bin := []byte("binary " + version)
	sum := sha256.Sum256(bin)
	o := ota.Offer{Schema: ota.OfferSchema, Program: "app", Version: version, IssuedAt: issued,
		Artifacts: []ota.Artifact{{OS: "linux", Arch: "amd64", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bin))}}}
	if mod != nil {
		mod(&o)
	}
	b, _ := json.Marshal(o)
	h.src.rels = append(h.src.rels, SourceRelease{Tag: "v" + version, Assets: []Asset{
		asset("ota-offer.json", b), asset("ota-offer.json.sig", ed25519.Sign(h.key, b)), asset("app_linux_amd64", bin),
	}})
	return b
}

func (h *harness) ingest() IngestResult {
	h.t.Helper()
	res, err := h.s.Ingest(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *harness) offered(client, channel string) string {
	h.t.Helper()
	v, ch, err := h.s.chooseVersion(context.Background(), ota.CheckRequest{Program: "app", ClientID: client, Channel: channel})
	if err != nil {
		h.t.Fatal(err)
	}
	if v == "" {
		return ""
	}
	o, err := h.s.offerFor(context.Background(), "app", v, ch, 0)
	if err != nil || o == nil {
		return ""
	}
	return v
}

func TestIngestVerifiesAndRejects(t *testing.T) {
	h := newHarness(t)
	h.release("1.8.0", 1000, nil)
	// A tampered offer: signature from the right key over different bytes.
	good := h.release("1.9.0", 2000, nil)
	rel := &h.src.rels[len(h.src.rels)-1]
	rel.Assets[0] = asset("ota-offer.json", bytes.Replace(good, []byte(`"1.9.0"`), []byte(`"9.9.9"`), 1))

	res := h.ingest()
	if res.Ingested != 1 || res.Rejected != 1 {
		t.Fatalf("result %+v", res)
	}
	rels, _ := h.store.Releases(context.Background(), "")
	var rejected *Release
	for i := range rels {
		if rels[i].Status == ReleaseRejected {
			rejected = &rels[i]
		}
	}
	if rejected == nil || !strings.Contains(rejected.Reason, "signature") {
		t.Fatalf("rejected release not recorded with its reason: %+v", rels)
	}
	if again := h.ingest(); again.Known != 1 || again.Ingested != 0 {
		t.Fatalf("re-ingest not idempotent: %+v", again)
	}
	// A rejected version cannot be rolled out.
	if err := h.s.SetRollout(context.Background(), Rollout{Program: "app", Channel: "stable", Version: "9.9.9", Percent: 10}); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("rollout of an unsigned version: %v", err)
	}
}

// A 10% rollout lands 10 ±3% of 1,000 clients, and the same ones every time.
func TestRolloutDistributionIsStable(t *testing.T) {
	h := newHarness(t)
	h.release("2.0.0", 1000, nil)
	h.ingest()
	if err := h.s.SetRollout(context.Background(), Rollout{Program: "app", Channel: "stable", Version: "2.0.0", Percent: 10, HaltThreshold: 0.05, HaltMinAttempts: 50}); err != nil {
		t.Fatal(err)
	}
	selected := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("client-%d", i)
		if h.offered(id, "stable") == "2.0.0" {
			selected[id] = true
		}
	}
	if n := len(selected); n < 70 || n > 130 {
		t.Fatalf("%d of 1000 selected at 10%%", n)
	}
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("client-%d", i)
		if (h.offered(id, "stable") == "2.0.0") != selected[id] {
			t.Fatalf("%s flipped between checks", id)
		}
	}
	if h.offered("client-1", "beta") != "" {
		t.Fatal("rollout leaked into another channel")
	}
}

func TestPinAndTargeterOverrideRollouts(t *testing.T) {
	h := newHarness(t)
	h.release("1.7.0", 1000, nil)
	h.release("2.0.0", 2000, nil)
	h.ingest()
	_ = h.s.SetRollout(context.Background(), Rollout{Program: "app", Channel: "stable", Version: "2.0.0", Percent: 100})
	if err := h.s.SetPin(context.Background(), Pin{Program: "app", ClientID: "pinned", Version: "1.7.0"}); err != nil {
		t.Fatal(err)
	}
	if got := h.offered("pinned", "stable"); got != "1.7.0" {
		t.Fatalf("pinned client offered %q", got)
	}
	if err := h.s.SetPin(context.Background(), Pin{Program: "app", ClientID: "x", Version: "3.0.0"}); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("pin to an unsigned version: %v", err)
	}
	h.s.opts.Targeter = targeterFunc(func(req ota.CheckRequest) (Target, bool) {
		if req.ClientID == "denied" {
			return Target{Deny: true}, true
		}
		return Target{}, false
	})
	if got := h.offered("denied", "stable"); got != "" {
		t.Fatalf("targeter deny ignored: %q", got)
	}
	if got := h.offered("other", "stable"); got != "2.0.0" {
		t.Fatalf("targeter fallthrough broke rollouts: %q", got)
	}
}

func TestExpiredOffersAreNotServedAndResignWins(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	h.s.opts.Now = func() time.Time { return now }
	h.release("2.0.0", now.Add(-48*time.Hour).UnixMilli(), func(o *ota.Offer) { o.ExpiresAt = now.Add(-time.Hour).UnixMilli() })
	h.ingest()
	_ = h.s.SetRollout(context.Background(), Rollout{Program: "app", Channel: "stable", Version: "2.0.0", Percent: 100})
	if got := h.offered("c", "stable"); got != "" {
		t.Fatalf("expired offer served: %q", got)
	}
	// The weekly re-sign lands as another offer for the same version.
	h.release("2.0.0", now.Add(-time.Minute).UnixMilli(), func(o *ota.Offer) { o.ExpiresAt = now.Add(720 * time.Hour).UnixMilli() })
	h.ingest()
	if got := h.offered("c", "stable"); got != "2.0.0" {
		t.Fatalf("re-signed offer not served: %q", got)
	}
}

type targeterFunc func(ota.CheckRequest) (Target, bool)

func (f targeterFunc) Target(_ context.Context, r ota.CheckRequest) (Target, bool, error) {
	t, ok := f(r)
	return t, ok, nil
}

// Failures past the threshold halt the rollout within one report.
func TestAutoHalt(t *testing.T) {
	h := newHarness(t)
	h.release("2.0.0", 1000, nil)
	h.ingest()
	_ = h.s.SetRollout(context.Background(), Rollout{Program: "app", Channel: "stable", Version: "2.0.0", Percent: 100, HaltThreshold: 0.05, HaltMinAttempts: 50})
	hs := httptest.NewServer(h.s.Handler())
	defer hs.Close()

	post := func(rs []ota.Report) {
		b, _ := json.Marshal(rs)
		resp, err := http.Post(hs.URL+"/v1/reports", "application/json", bytes.NewReader(b))
		if err != nil || resp.StatusCode != http.StatusAccepted {
			t.Fatalf("post reports: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	var rs []ota.Report
	for i := 0; i < 47; i++ {
		rs = append(rs, ota.Report{ClientID: fmt.Sprint("ok-", i), Program: "app", Version: "2.0.0", Stage: ota.StageInstalled})
	}
	post(rs)
	post([]ota.Report{
		{ClientID: "bad-1", Program: "app", Version: "2.0.0", Stage: ota.StageRolledBack},
		{ClientID: "bad-2", Program: "app", Version: "2.0.0", Stage: ota.StageFailed},
	})
	if ro, _ := h.store.Rollouts(context.Background(), "app"); ro[0].State != RolloutActive {
		t.Fatal("halted below the minimum number of attempts")
	}
	post([]ota.Report{
		{ClientID: "bad-3", Program: "app", Version: "2.0.0", Stage: ota.StageRolledBack},
		// A repeat of an earlier report must not count twice.
		{ClientID: "bad-3", Program: "app", Version: "2.0.0", Stage: ota.StageRolledBack},
	})
	ro, _ := h.store.Rollouts(context.Background(), "app")
	if ro[0].State != RolloutHalted || !strings.Contains(ro[0].Reason, "3 of 50") {
		t.Fatalf("rollout = %+v", ro[0])
	}
	if h.offered("fresh-client", "stable") != "" {
		t.Fatal("halted rollout still offered")
	}
}

func TestAdminAPIRequiresAuthAndValidates(t *testing.T) {
	h := newHarness(t)
	h.release("2.0.0", 1000, nil)
	hs := httptest.NewServer(h.s.Handler())
	defer hs.Close()
	do := func(method, path, body string, auth bool) int {
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer admin")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := do("POST", "/v1/admin/ingest", "", false); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated ingest: %d", code)
	}
	if code := do("POST", "/v1/admin/ingest", "", true); code != http.StatusOK {
		t.Fatalf("ingest: %d", code)
	}
	if code := do("PUT", "/v1/admin/rollouts/app/stable", `{"version":"2.0.0","percent":25}`, true); code != http.StatusOK {
		t.Fatalf("rollout: %d", code)
	}
	if code := do("PUT", "/v1/admin/rollouts/app/stable", `{"version":"3.0.0","percent":25}`, true); code != http.StatusConflict {
		t.Fatalf("rollout of unsigned version: %d, want 409", code)
	}
	if code := do("PUT", "/v1/admin/rollouts/app/stable", `{"version":"2.0.0","percent":250}`, true); code != http.StatusBadRequest {
		t.Fatalf("invalid percent: %d, want 400", code)
	}

	// Without AuthorizeAdmin the admin API does not exist at all.
	s2, _ := New(Options{Store: NewMemStore(), Blobs: FSBlobStore{Dir: t.TempDir()}})
	hs2 := httptest.NewServer(s2.Handler())
	defer hs2.Close()
	resp, _ := http.Post(hs2.URL+"/v1/admin/ingest", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("admin API reachable without AuthorizeAdmin: %d", resp.StatusCode)
	}
}

func TestAuthorizedClientIDOverridesTheBody(t *testing.T) {
	h := newHarness(t)
	h.s.opts.Authorize = func(*http.Request) (string, error) { return "real-agent", nil }
	hs := httptest.NewServer(h.s.Handler())
	defer hs.Close()
	b, _ := json.Marshal([]ota.Report{{ClientID: "spoofed", Program: "app", Version: "1.0.0", Stage: ota.StageCommitted, At: time.Now().UnixMilli()}})
	resp, _ := http.Post(hs.URL+"/v1/reports", "application/json", bytes.NewReader(b))
	resp.Body.Close()
	cs, _ := h.store.Clients(context.Background(), "app")
	if len(cs) != 1 || cs[0].ClientID != "real-agent" {
		t.Fatalf("clients = %+v", cs)
	}
}

// Package fixture builds sampleapp as v1 and v2, installs v1 and serves v2, for
// the suites that exercise real binaries. Test-only: it imports testing and
// httptest, which is why it is not part of testkit, which production code
// imports for its kill hooks.
package fixture

import (
	"bytes"
	"context"
	stded "crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/server"
	"github.com/ArmornetIO/ota/verify/ed25519"
)

// ModuleRoot is the ota module directory, found by walking up from the test's
// working directory.
func ModuleRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && strings.Contains(string(b), "module github.com/ArmornetIO/ota\n") { // #nosec G304 -- test kit walking up to its own module root
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("ota module root not found")
		}
		dir = parent
	}
}

// BuildSampleApp builds testdata/sampleapp with the otatest tag and returns
// its bytes.
func BuildSampleApp(t *testing.T, out, version, behaviour string) []byte {
	t.Helper()
	cmd := exec.Command("go", "build", "-tags", "otatest", // #nosec G204 -- test kit compiling its own sample app
		"-ldflags", "-X main.version="+version+" -X main.behaviour="+behaviour,
		"-o", out, "./testdata/sampleapp")
	cmd.Dir = ModuleRoot(t)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sampleapp %s: %v\n%s", version, err, b)
	}
	b, err := os.ReadFile(out) // #nosec G304 -- the binary the test kit just built
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Fixture is sampleapp v1 installed, and v2 offered by a real server.
type Fixture struct {
	T      *testing.T
	Bin    string
	V1, V2 []byte
	Marker string
	Env    []string
	Store  *server.MemStore
}

// NewFixture builds v1 and v2, installs v1, and serves an offer for v2 signed
// by signWith (a fresh trusted key when nil).
func NewFixture(t *testing.T, v2behaviour string, signWith stded.PrivateKey) *Fixture {
	t.Helper()
	if testing.Short() {
		t.Skip("builds binaries")
	}
	// These suites stop children with SIGTERM and observe an exec'd process
	// replacing itself; Windows has neither, so the harness itself cannot run
	// there. Windows swap, lock and service behaviour is covered by the unit
	// tests in internal/stage, internal/swap and service instead.
	if runtime.GOOS == "windows" {
		t.Skip("binary-level suites need signals and exec; see the unit tests for Windows coverage")
	}
	pub, priv, _ := stded.GenerateKey(nil)
	if signWith == nil {
		signWith = priv
	}
	work := t.TempDir()
	f := &Fixture{T: t}
	f.V1 = BuildSampleApp(t, filepath.Join(work, "v1"), "1.0.0", "ok")
	f.V2 = BuildSampleApp(t, filepath.Join(work, "v2"), "2.0.0", v2behaviour)

	f.Bin = filepath.Join(t.TempDir(), "sampleapp")
	if runtime.GOOS == "windows" {
		f.Bin += ".exe"
	}
	if err := os.WriteFile(f.Bin, f.V1, 0o755); err != nil { // #nosec G306 -- an executable test fixture needs the execute bit
		t.Fatal(err)
	}
	f.Marker = filepath.Join(work, "marker")

	sum := sha256.Sum256(f.V2)
	sha := hex.EncodeToString(sum[:])
	blobs := server.FSBlobStore{Dir: filepath.Join(work, "blobs")}
	if err := blobs.Put(context.Background(), sha, bytes.NewReader(f.V2)); err != nil {
		t.Fatal(err)
	}
	offer, _ := json.Marshal(ota.Offer{
		Schema: ota.OfferSchema, Program: "sampleapp", Version: "2.0.0",
		IssuedAt:  time.Now().Add(-time.Minute).UnixMilli(),
		Artifacts: []ota.Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: sha, Size: int64(len(f.V2))}},
	})
	f.Store = server.NewMemStore()
	_ = f.Store.PutOffer(ota.SignedOffer{Offer: offer, Signature: ed25519.Sign(signWith, offer), Kind: ed25519.Kind})
	_ = f.Store.PutRollout(context.Background(), server.Rollout{
		Program: "sampleapp", Channel: "stable", Version: "2.0.0", Percent: 100,
		HaltThreshold: 1, State: server.RolloutActive,
	})
	srv, err := server.New(server.Options{Store: f.Store, Blobs: blobs})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	f.Env = append(os.Environ(),
		"OTA_SERVER="+hs.URL,
		"OTA_PUBKEY="+base64.StdEncoding.EncodeToString(pub),
		"OTA_MARKER="+f.Marker,
	)
	return f
}

// Command returns an exec.Cmd for the installed binary with the fixture env.
func (f *Fixture) Command(args ...string) *exec.Cmd {
	cmd := exec.Command(f.Bin, args...) // #nosec G204 -- runs the binary the test kit built into its own temp dir
	cmd.Env = f.Env
	return cmd
}

// Lines returns the marker file.
func (f *Fixture) Lines() string {
	b, _ := os.ReadFile(f.Marker)
	return string(b)
}

// Installed returns the bytes at the install path.
func (f *Fixture) Installed() []byte {
	b, err := os.ReadFile(f.Bin)
	if err != nil {
		f.T.Fatalf("no binary at the install path: %v", err)
	}
	return b
}

// WaitFor polls the marker file until it contains s.
func (f *Fixture) WaitFor(s string, timeout time.Duration) bool { return f.WaitForCount(s, 1, timeout) }

// WaitForCount polls the marker file until s appears at least n times.
func (f *Fixture) WaitForCount(s string, n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Count(f.Lines(), s) >= n {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

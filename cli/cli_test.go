package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/cli"
	"github.com/ArmornetIO/ota/internal/stage"
	"github.com/ArmornetIO/ota/internal/swap"
	"github.com/ArmornetIO/ota/internal/testkit/fixture"
)

func update(t *testing.T, f *fixture.Fixture, args ...string) (int, map[string]any) {
	t.Helper()
	cmd := f.Command(append([]string{"update", "-json", "-wait", "20s"}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	_ = cmd.Run()
	res := map[string]any{}
	_ = json.Unmarshal(out.Bytes(), &res)
	return cmd.ProcessState.ExitCode(), res
}

func TestExitCodeMapping(t *testing.T) {
	cases := map[error]int{
		nil:                                  cli.ExitOK,
		ota.ErrDeferred:                      cli.ExitDeferred,
		ota.ErrDrainTimeout:                  cli.ExitDrainTimeout,
		ota.ErrRefused:                       cli.ExitRefused,
		fmt.Errorf("x: %w", ota.ErrDownload): cli.ExitDownload,
		ota.ErrRolledBack:                    cli.ExitRolledBack,
		ota.ErrLocked:                        cli.ExitLocked,
		ota.ErrRestartNeeded:                 cli.ExitRestartNeeded,
		fmt.Errorf("anything else"):          cli.ExitError,
	}
	for err, want := range cases {
		if got := cli.ExitCode(err); got != want {
			t.Errorf("ExitCode(%v) = %d, want %d", err, got, want)
		}
	}
}

// With nothing running the program, `update` installs the new version for the
// next start (exit 60), and that start's Boot gates and commits it.
func TestUpdateWithNothingRunningStagesForNextStart(t *testing.T) {
	f := fixture.NewFixture(t, "ok", nil)

	if code, res := update(t, f, "-dry-run"); code != cli.ExitOK || res["result"] != string(ota.StageOfferSeen) {
		t.Fatalf("dry run: exit %d %v", code, res)
	}
	if !bytes.Equal(f.Installed(), f.V1) {
		t.Fatal("dry run changed the binary")
	}

	code, res := update(t, f)
	if code != cli.ExitRestartNeeded || res["result"] != "staged" {
		t.Fatalf("exit %d %v", code, res)
	}
	if !bytes.Equal(f.Installed(), f.V2) {
		t.Fatal("v2 not installed for the next start")
	}
	// The deadline must not have started: a staged update can wait for days.
	p, _ := swap.LoadPending(swap.For(f.Bin))
	if p == nil || p.Deadline != 0 {
		t.Fatalf("pending = %+v, want an unstarted deadline", p)
	}

	if err := f.Command().Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.Lines(), "gate committed 2.0.0") {
		t.Fatalf("next start did not commit v2\n%s", f.Lines())
	}
	if code, res := update(t, f); code != cli.ExitOK || res["result"] != "uptodate" {
		t.Fatalf("after commit: exit %d %v", code, res)
	}
}

func TestUpdateRefusedAndLockedAndUnreachable(t *testing.T) {
	_, other, _ := genKey()
	f := fixture.NewFixture(t, "ok", other)
	if code, _ := update(t, f); code != cli.ExitRefused {
		t.Fatalf("untrusted offer: exit %d", code)
	}

	lock, err := stage.Acquire(swap.For(f.Bin).StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := update(t, f); code != cli.ExitLocked {
		t.Fatalf("lock held: exit %d", code)
	}
	lock.Release()

	for i, e := range f.Env {
		if strings.HasPrefix(e, "OTA_SERVER=") {
			f.Env[i] = "OTA_SERVER=http://127.0.0.1:1"
		}
	}
	if code, _ := update(t, f); code != cli.ExitError {
		t.Fatalf("unreachable server: exit %d", code)
	}
	if !bytes.Equal(f.Installed(), f.V1) {
		t.Fatal("binary changed")
	}
}

// startLauncher runs `sampleapp run -- serve` and waits for the child to boot.
func startLauncher(t *testing.T, f *fixture.Fixture, args ...string) (*exec.Cmd, chan int) {
	t.Helper()
	cmd := f.Command(append(append([]string{"run"}, args...), "--", "serve")...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		_ = cmd.Wait()
		done <- cmd.ProcessState.ExitCode()
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, done
}

func stopLauncher(t *testing.T, cmd *exec.Cmd, done chan int) int {
	t.Helper()
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case code := <-done:
		return code
	case <-time.After(15 * time.Second):
		t.Fatal("launcher did not stop")
		return -1
	}
}

// Scenario 8b: `update` hands the new version to a running launcher, which
// restarts its child on it; after commit the launcher moves onto the new
// version itself without restarting the child again.
func TestUpdateHandsOffToRunningLauncher(t *testing.T) {
	f := fixture.NewFixture(t, "ok", nil)
	cmd, done := startLauncher(t, f)
	if !f.WaitFor("boot 1.0.0", 10*time.Second) {
		t.Fatalf("child never booted\n%s", f.Lines())
	}

	code, res := update(t, f)
	if code != cli.ExitOK || res["path"] != "launcher" || res["result"] != string(ota.StageCommitted) {
		t.Fatalf("update: exit %d %v\n%s", code, res, f.Lines())
	}
	if !bytes.Equal(f.Installed(), f.V2) {
		t.Fatal("v2 not installed")
	}
	// Give the launcher a moment to re-exec onto v2; the child must not restart.
	time.Sleep(500 * time.Millisecond)
	if n := strings.Count(f.Lines(), "boot 2.0.0"); n != 1 {
		t.Fatalf("child started %d times on v2 (re-exec must adopt it)\n%s", n, f.Lines())
	}
	if code := stopLauncher(t, cmd, done); code != 0 {
		t.Fatalf("launcher exit %d\n%s", code, f.Lines())
	}
	if !strings.Contains(f.Lines(), "stopped 2.0.0") {
		t.Fatalf("child was not stopped cleanly\n%s", f.Lines())
	}
}

// A new version that crash-loops under the launcher is rolled back by its
// own Boot after MaxAttempts, and the launcher keeps the old version running.
func TestLauncherRollsBackCrashLoopingVersion(t *testing.T) {
	f := fixture.NewFixture(t, "crash", nil)
	cmd, done := startLauncher(t, f, "-supervise", "always")
	if !f.WaitFor("boot 1.0.0", 10*time.Second) {
		t.Fatalf("child never booted\n%s", f.Lines())
	}
	code, res := update(t, f)
	if code != cli.ExitRolledBack {
		t.Fatalf("update: exit %d %v\n%s", code, res, f.Lines())
	}
	if !bytes.Equal(f.Installed(), f.V1) {
		t.Fatal("crash-looping v2 left installed")
	}
	if !f.WaitFor("event rolled_back", 5*time.Second) || !f.WaitForCount("boot 1.0.0", 2, 5*time.Second) {
		t.Fatalf("v1 not restarted after rollback\n%s", f.Lines())
	}
	stopLauncher(t, cmd, done)
}

func TestLauncherGivesUpOnAChildThatAlwaysCrashes(t *testing.T) {
	f := fixture.NewFixture(t, "crash", nil)
	if err := os.WriteFile(f.Bin, f.V2, 0o755); err != nil { // install the crashing build directly
		t.Fatal(err)
	}
	_, done := startLauncher(t, f, "-supervise", "always", "-max-crashes", "3")
	select {
	case code := <-done:
		if code != 3 {
			t.Fatalf("launcher exit %d, want the child's 3", code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("launcher never gave up")
	}
	if n := strings.Count(f.Lines(), "boot 2.0.0"); n != 3 {
		t.Fatalf("child started %d times, want 3", n)
	}
}

func TestOfferFromGoreleaserDist(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	bin := filepath.Join(dist, "app_linux_amd64")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(bin, []byte("linux binary"), 0o755)
	_ = os.WriteFile(filepath.Join(dist, "metadata.json"), []byte(`{"project_name":"app","tag":"v1.8.0","version":"1.8.0"}`), 0o644)
	_ = os.WriteFile(filepath.Join(dist, "artifacts.json"), []byte(`[
	  {"name":"app","path":"dist/app_linux_amd64_v1/app","goos":"linux","goarch":"amd64","type":"Binary","extra":{"ID":"app"}},
	  {"name":"app_linux_amd64","path":"dist/app_linux_amd64","goos":"linux","goarch":"amd64","type":"Archive","extra":{"Format":"binary","ID":"app"}},
	  {"name":"checksums.txt","path":"dist/checksums.txt","type":"Checksum"}
	]`), 0o644)

	out := filepath.Join(root, "ota-offer.json")
	code := cli.Commands(nil)["offer"]([]string{
		"-dist", dist, "-channels", "stable", "-out", out, "-issued-at", "1789776000000",
		"-url-template", "https://github.com/o/r/releases/download/{tag}/{name}",
	})
	if code != 0 {
		t.Fatalf("offer exit %d", code)
	}
	b, _ := os.ReadFile(out)
	o, err := ota.ParseOffer(b)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := o.ArtifactFor("linux", "amd64")
	if !ok || o.Program != "app" || o.Version != "1.8.0" || o.IssuedAt != 1789776000000 || o.Channels[0] != "stable" {
		t.Fatalf("offer = %+v", o)
	}
	if a.Size != int64(len("linux binary")) || a.URLs[0] != "https://github.com/o/r/releases/download/v1.8.0/app_linux_amd64" {
		t.Fatalf("artifact = %+v", a)
	}

	// From checksums.txt, as goreleaser's signs step sees it.
	_ = os.WriteFile(filepath.Join(dist, "app_darwin_arm64"), []byte("mac binary"), 0o755)
	_ = os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(
		a.SHA256+"  app_linux_amd64\n"+
			strings.Repeat("b", 64)+"  app_darwin_arm64\n"+
			strings.Repeat("c", 64)+"  app_1.8.0_linux_amd64.tar.gz\n"), 0o644)
	cs := filepath.Join(root, "from-checksums.json")
	if code := cli.Commands(nil)["offer"]([]string{"-checksums", filepath.Join(dist, "checksums.txt"),
		"-program", "app", "-version", "v1.8.0", "-out", cs,
		"-url-template", "https://example.com/{tag}/{name}"}); code != 0 {
		t.Fatalf("checksums offer exit %d", code)
	}
	b, _ = os.ReadFile(cs)
	co, err := ota.ParseOffer(b)
	if err != nil || len(co.Artifacts) != 2 {
		t.Fatalf("checksums offer = %+v, %v", co, err)
	}
	if mac, ok := co.ArtifactFor("darwin", "arm64"); !ok || mac.Size != int64(len("mac binary")) || mac.URLs[0] != "https://example.com/v1.8.0/app_darwin_arm64" {
		t.Fatalf("mac artifact = %+v", mac)
	}

	// A rollback offer reuses an older release's artifacts under a new issue time.
	rb := filepath.Join(root, "rollback.json")
	if code := cli.Commands(nil)["offer"]([]string{"-from", out, "-downgrade", "-out", rb}); code != 0 {
		t.Fatalf("rollback offer exit %d", code)
	}
	b, _ = os.ReadFile(rb)
	r, err := ota.ParseOffer(b)
	if err != nil || !r.Downgrade || r.IssuedAt <= o.IssuedAt || r.Artifacts[0].SHA256 != a.SHA256 {
		t.Fatalf("rollback offer = %+v, %v", r, err)
	}
}

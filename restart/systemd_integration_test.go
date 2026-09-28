//go:build integration && linux

package restart

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArmornetIO/ota/health"
	"github.com/ArmornetIO/ota/internal/testkit/fixture"
	"github.com/ArmornetIO/ota/service"
)

// These run against the real `systemctl --user`: go test -tags integration ./restart/ on a Linux host with a user
// systemd instance.

func requireUserSystemd(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("systemctl", "--user", "is-system-running").CombinedOutput(); err != nil &&
		string(out) != "degraded\n" {
		t.Skipf("no user systemd: %v %s", err, out)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func get(url string) (string, error) {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func waitVersion(t *testing.T, url, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := get(url); got == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never served %s", url, want)
}

func TestServiceInPlaceUnderSystemd(t *testing.T) {
	requireUserSystemd(t)
	root := t.TempDir()
	bin := filepath.Join(root, "sampleapp")
	fixture.BuildSampleApp(t, bin, "1.0.0", "ok")
	v2 := filepath.Join(root, "v2")
	fixture.BuildSampleApp(t, v2, "2.0.0", "ok")
	port := freePort(t)
	spec := service.Spec{Name: "ota-it-inplace", BinaryPath: bin, Args: []string{"serve", "-listen", "127.0.0.1:" + port}}
	if err := service.Install(spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Uninstall(spec) })
	url := "http://127.0.0.1:" + port + "/"
	waitVersion(t, url, "1.0.0")

	p := Plan{Binary: bin, Version: "2.0.0",
		Install: func() error { return os.Rename(v2, bin) },
		Restore: func() error { return nil },
	}
	if err := run(t, ServiceInPlace(spec, nil).Chain(p)); err != nil {
		t.Fatal(err)
	}
	waitVersion(t, url, "2.0.0")
}

// A program that shares its port sees zero failed requests while the
// new version starts beside the old and the old is removed.
func TestBlueGreenUnderSystemdHasNoFailedRequests(t *testing.T) {
	requireUserSystemd(t)
	root := t.TempDir()
	for _, v := range []string{"1.0.0"} {
		_ = os.MkdirAll(VersionDir(root, v), 0o755)
		fixture.BuildSampleApp(t, filepath.Join(VersionDir(root, v), "sampleapp"), v, "ok")
	}
	staged := filepath.Join(root, ".ota-staged")
	fixture.BuildSampleApp(t, staged, "2.0.0", "ok")
	if err := setCurrent(root, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	url := "http://127.0.0.1:" + port + "/"
	spec := func(v, bin string) service.Spec {
		return service.Spec{Name: "ota-it-bg-" + v, BinaryPath: bin, Args: []string{"serve", "-listen", "127.0.0.1:" + port}}
	}
	old := spec("1.0.0", filepath.Join(VersionDir(root, "1.0.0"), "sampleapp"))
	if err := service.Install(old); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Uninstall(old)
		_ = service.Uninstall(spec("2.0.0", ""))
	})
	waitVersion(t, url, "1.0.0")

	var failed, total atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			total.Add(1)
			if _, err := get(url); err != nil {
				failed.Add(1)
			}
		}
	}()

	bg := ServiceBlueGreen(BlueGreenOptions{
		Root: root, Binary: "sampleapp", CurrentVersion: "1.0.0", Spec: spec,
		Probe:        func(string) health.Source { return health.ProbeURL("http://127.0.0.1:" + port + "/healthz") },
		SteadyWindow: time.Second, Deadline: 30 * time.Second, Poll: 100 * time.Millisecond,
	})
	p := Plan{Version: "2.0.0", Staged: staged, Commit: func() error { return nil }}
	if err := run(t, bg.Chain(p)); err != nil {
		t.Fatal(err)
	}
	waitVersion(t, url, "2.0.0")
	time.Sleep(time.Second)
	cancel()
	if failed.Load() != 0 {
		t.Fatalf("%d of %d requests failed during the switch", failed.Load(), total.Load())
	}
	if st, err := service.Query(old); err == nil && st.Installed {
		t.Fatal("old version's service still installed")
	}
}

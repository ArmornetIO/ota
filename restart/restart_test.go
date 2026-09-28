package restart

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArmornetIO/ota/health"
	"github.com/ArmornetIO/ota/internal/stage"
	"github.com/ArmornetIO/ota/lifecycle"
	"github.com/ArmornetIO/ota/service"
)

type fakeBackend struct {
	mu        sync.Mutex
	calls     []string
	installed map[string]bool
}

func newFakeBackend(installed ...string) *fakeBackend {
	b := &fakeBackend{installed: map[string]bool{}}
	for _, n := range installed {
		b.installed[n] = true
	}
	return b
}

func (b *fakeBackend) rec(op string, s service.Spec) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, op+":"+s.Name)
}

func (b *fakeBackend) Install(s service.Spec) error {
	b.rec("install", s)
	b.installed[s.Name] = true
	return nil
}

func (b *fakeBackend) Uninstall(s service.Spec) error {
	b.rec("uninstall", s)
	delete(b.installed, s.Name)
	return nil
}

func (b *fakeBackend) Query(s service.Spec) (service.Status, error) {
	b.rec("query", s)
	if !b.installed[s.Name] {
		return service.Status{}, service.ErrNotInstalled
	}
	return service.Status{Installed: true, Running: true}, nil
}
func (b *fakeBackend) Start(s service.Spec) error   { b.rec("start", s); return nil }
func (b *fakeBackend) Stop(s service.Spec) error    { b.rec("stop", s); return nil }
func (b *fakeBackend) Restart(s service.Spec) error { b.rec("restart", s); return nil }

// planRecorder builds a Plan whose closures record what was called.
type planRecorder struct {
	calls []string
	p     Plan
}

func newPlan(version string) *planRecorder {
	r := &planRecorder{}
	r.p = Plan{
		Binary:        "/opt/app/bin/app",
		Version:       version,
		Install:       func() error { r.calls = append(r.calls, "install"); return nil },
		Restore:       func() error { r.calls = append(r.calls, "restore"); return nil },
		BeforeRestart: func() { r.calls = append(r.calls, "before-restart") },
		DeferGate:     func() error { r.calls = append(r.calls, "defer-gate"); return nil },
		Commit:        func() error { r.calls = append(r.calls, "commit"); return nil },
	}
	return r
}

func run(t *testing.T, hooks []lifecycle.Hook) error {
	t.Helper()
	var m lifecycle.Manager
	for _, h := range hooks {
		m.Append(h)
	}
	return m.Start(context.Background())
}

func TestServiceInPlaceSwapsThenRestartsTheService(t *testing.T) {
	b := newFakeBackend("app")
	r := newPlan("2.0.0")
	if err := run(t, ServiceInPlace(service.Spec{Name: "app"}, b).Chain(r.p)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.calls, []string{"install", "before-restart"}) || !slices.Contains(b.calls, "restart:app") {
		t.Fatalf("plan calls %v, backend calls %v", r.calls, b.calls)
	}
}

func TestHandOffPaths(t *testing.T) {
	t.Run("launcher", func(t *testing.T) {
		dir := t.TempDir()
		l, err := stage.AcquireNamed(dir, stage.LauncherLock)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Release()
		h := &HandOff{StateDir: dir, Service: &service.Spec{Name: "app"}, Backend: newFakeBackend("app")}
		if err := run(t, h.Chain(newPlan("2.0.0").p)); err != nil {
			t.Fatal(err)
		}
		if h.Path() != PathLauncher {
			t.Fatalf("path = %s", h.Path())
		}
		if _, err := os.Stat(filepath.Join(dir, RestartRequest)); err != nil {
			t.Fatal("no restart request written for the launcher")
		}
	})
	t.Run("service", func(t *testing.T) {
		b := newFakeBackend("app")
		h := &HandOff{StateDir: t.TempDir(), Service: &service.Spec{Name: "app"}, Backend: b}
		if err := run(t, h.Chain(newPlan("2.0.0").p)); err != nil {
			t.Fatal(err)
		}
		if h.Path() != PathService || !slices.Contains(b.calls, "restart:app") {
			t.Fatalf("path = %s, calls %v", h.Path(), b.calls)
		}
	})
	t.Run("staged", func(t *testing.T) {
		r := newPlan("2.0.0")
		h := &HandOff{StateDir: t.TempDir(), Service: &service.Spec{Name: "app"}, Backend: newFakeBackend()}
		if err := run(t, h.Chain(r.p)); err != nil {
			t.Fatal(err)
		}
		if h.Path() != PathStaged || !slices.Contains(r.calls, "defer-gate") {
			t.Fatalf("path = %s, calls %v", h.Path(), r.calls)
		}
	})
}

func blueGreenSetup(t *testing.T, status string) (BlueGreenOptions, *fakeBackend, *planRecorder, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(VersionDir(root, "1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(VersionDir(root, "1.0.0"), "app"), []byte("v1"), 0o755)
	if err := setCurrent(root, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(root, ".ota-staged")
	_ = os.WriteFile(staged, []byte("v2"), 0o600)
	b := newFakeBackend("app-1.0.0")
	o := BlueGreenOptions{
		Root: root, Binary: "app", CurrentVersion: "1.0.0", Backend: b,
		Spec: func(v, bin string) service.Spec {
			return service.Spec{Name: "app-" + v, BinaryPath: bin}
		},
		Probe:        func(string) health.Source { return health.StatusFunc(func() string { return status }) },
		SteadyWindow: time.Millisecond, Deadline: time.Second, Poll: time.Millisecond,
	}
	r := newPlan("2.0.0")
	r.p.Staged = staged
	return o, b, r, root
}

func TestBlueGreenSwitchesOnlyAfterTheNewVersionIsHealthy(t *testing.T) {
	o, b, r, root := blueGreenSetup(t, health.StatusOK)
	if err := run(t, ServiceBlueGreen(o).Chain(r.p)); err != nil {
		t.Fatal(err)
	}
	if cur, _ := Current(root); cur != "2.0.0" {
		t.Fatalf("current = %q", cur)
	}
	got, _ := os.ReadFile(filepath.Join(VersionDir(root, "2.0.0"), "app"))
	if string(got) != "v2" {
		t.Fatal("new version not placed")
	}
	// The old service is only removed after the new one was installed and gated.
	want := []string{"install:app-2.0.0", "uninstall:app-1.0.0"}
	if !slices.Equal(b.calls, want) {
		t.Fatalf("backend calls %v, want %v", b.calls, want)
	}
	if !slices.Contains(r.calls, "commit") {
		t.Fatal("attempt not committed")
	}
}

func TestBlueGreenUnhealthyLeavesTheOldVersionUntouched(t *testing.T) {
	o, b, r, root := blueGreenSetup(t, health.StatusFatalError)
	if err := run(t, ServiceBlueGreen(o).Chain(r.p)); err == nil || !strings.Contains(err.Error(), "health gate") {
		t.Fatalf("err = %v", err)
	}
	if cur, _ := Current(root); cur != "1.0.0" {
		t.Fatalf("current moved to %q", cur)
	}
	if _, err := os.Stat(VersionDir(root, "2.0.0")); !os.IsNotExist(err) {
		t.Fatal("failed version directory kept")
	}
	want := []string{"install:app-2.0.0", "uninstall:app-2.0.0"}
	if !slices.Equal(b.calls, want) {
		t.Fatalf("backend calls %v, want %v (the old service must never be touched)", b.calls, want)
	}
	if slices.Contains(r.calls, "commit") {
		t.Fatal("unhealthy version committed")
	}
}

func TestExternallySupervised(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")
	t.Setenv("XPC_SERVICE_NAME", "0")
	if externallySupervised() {
		t.Fatal("reported supervised with no service manager variables")
	}
	t.Setenv("INVOCATION_ID", "abc")
	if !externallySupervised() {
		t.Fatal("systemd not detected")
	}
}

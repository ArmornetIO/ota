package swap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArmornetIO/ota/internal/stage"
)

func setup(t *testing.T) (Layout, string) {
	t.Helper()
	dir := t.TempDir()
	l := For(filepath.Join(dir, "app"))
	if err := os.WriteFile(l.Binary, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := stage.EnsureDir(l.StateDir); err != nil {
		t.Fatal(err)
	}
	f, err := stage.CreateTemp(l.StateDir, ".part")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("v2")
	f.Close()
	return l, f.Name()
}

func content(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallThenCommit(t *testing.T) {
	l, newPath := setup(t)
	if err := Install(l, newPath, Pending{Version: "2.0.0", Deadline: time.Now().Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if content(t, l.Binary) != "v2" || content(t, l.Prev()) != "v1" {
		t.Fatal("swap did not install v2 and keep v1")
	}
	if fi, _ := os.Stat(l.Binary); fi.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %v, want the old binary's 0755", fi.Mode().Perm())
	}
	if p, _ := LoadPending(l); p == nil || p.Version != "2.0.0" {
		t.Fatal("pending not recorded")
	}
	if err := Commit(l); err != nil {
		t.Fatal(err)
	}
	if p, _ := LoadPending(l); p != nil {
		t.Fatal("pending survived commit")
	}
	if _, err := os.Stat(l.Prev()); !os.IsNotExist(err) {
		t.Fatal("previous binary survived commit")
	}
}

func TestInstallThenRestore(t *testing.T) {
	l, newPath := setup(t)
	if err := Install(l, newPath, Pending{Version: "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := Restore(l); err != nil {
		t.Fatal(err)
	}
	if content(t, l.Binary) != "v1" {
		t.Fatal("restore did not bring back v1")
	}
	if p, _ := LoadPending(l); p != nil {
		t.Fatal("pending survived restore")
	}
}

// The crash Windows is exposed to: the running binary moved aside and the new
// one not yet in place. Repair must leave a runnable binary behind.
func TestRepairAfterCrashBetweenRenames(t *testing.T) {
	l, _ := setup(t)
	_ = SavePending(l, Pending{Version: "2.0.0"})
	if err := os.Rename(l.Binary, l.Prev()); err != nil {
		t.Fatal(err)
	}
	repaired, err := Repair(l)
	if err != nil || !repaired {
		t.Fatalf("Repair = %v, %v", repaired, err)
	}
	if content(t, l.Binary) != "v1" {
		t.Fatal("repair did not restore v1")
	}
	if p, _ := LoadPending(l); p != nil {
		t.Fatal("pending survived repair")
	}
}

func TestRepairNoopWhenBinaryPresent(t *testing.T) {
	l, _ := setup(t)
	if repaired, err := Repair(l); err != nil || repaired {
		t.Fatalf("Repair = %v, %v", repaired, err)
	}
}

// The displaced previous binary is cleaned up at the next start once
// nothing is pending, which is how Windows gets rid of a file it could not
// delete while the old process still ran from it.
func TestCleanRemovesPrevOnlyWhenNothingPending(t *testing.T) {
	l, newPath := setup(t)
	_ = Install(l, newPath, Pending{Version: "2.0.0"})
	Clean(l)
	if _, err := os.Stat(l.Prev()); err != nil {
		t.Fatal("Clean removed prev while an attempt was pending")
	}
	_ = stage.Remove(filepath.Join(l.StateDir, "pending.json"))
	_ = os.WriteFile(filepath.Join(l.StateDir, "bad-1"), nil, 0o600)
	Clean(l)
	if _, err := os.Stat(l.Prev()); !os.IsNotExist(err) {
		t.Fatal("Clean kept prev with nothing pending")
	}
	if _, err := os.Stat(filepath.Join(l.StateDir, "bad-1")); !os.IsNotExist(err) {
		t.Fatal("Clean kept a displaced binary")
	}
}

func TestLayoutIsPerBinary(t *testing.T) {
	a, b := For("/usr/local/bin/one"), For("/usr/local/bin/two")
	if a.StateDir == b.StateDir {
		t.Fatal("two binaries in one directory share state")
	}
}

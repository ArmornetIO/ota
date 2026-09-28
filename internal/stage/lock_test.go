package stage

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ArmornetIO/ota"
)

func TestSecondLockerIsRefused(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ota.ErrLocked) {
		t.Fatalf("second Acquire err = %v, want ErrLocked", err)
	}
	l.Release()
	l2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	l2.Release()
}

// The lock must not outlive a crashed holder, or a single kill -9 would wedge
// updates on that machine forever.
func TestLockReleasedWhenHolderDies(t *testing.T) {
	if os.Getenv("OTA_LOCK_HOLDER") != "" {
		if _, err := Acquire(os.Getenv("OTA_LOCK_HOLDER")); err != nil {
			os.Exit(2)
		}
		os.Stdout.WriteString("held\n")
		time.Sleep(time.Minute)
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestLockReleasedWhenHolderDies")
	cmd.Env = append(os.Environ(), "OTA_LOCK_HOLDER="+dir)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := out.Read(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ota.ErrLocked) {
		t.Fatalf("lock not held by child: %v", err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatalf("lock still held after holder died: %v", err)
	}
	l.Release()
}

func TestSweepKeepsActivePartialOnly(t *testing.T) {
	dir := t.TempDir()
	keep, _ := CreateTemp(dir, ".part")
	stale, _ := CreateTemp(dir, ".part")
	keep.Close()
	stale.Close()
	other := filepath.Join(dir, "state.json")
	_ = os.WriteFile(other, []byte("{}"), 0o600)

	if err := Sweep(dir, keep.Name()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keep.Name()); err != nil {
		t.Fatal("active partial swept")
	}
	if _, err := os.Stat(stale.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale partial kept")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("non-temp file swept")
	}
}

func TestStateDirIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := filepath.Join(t.TempDir(), "state")
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700", fi.Mode().Perm())
	}
	f, _ := CreateTemp(dir, ".part")
	f.Close()
	fi, _ = os.Stat(f.Name())
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("temp mode = %v, want 0600", fi.Mode().Perm())
	}
}

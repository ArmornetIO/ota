// Package stage owns everything that touches disk before a swap: the
// single-updater lock, the state directory, free-space checks, and the
// streamed, resumable, hash-verified download.
package stage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ArmornetIO/ota"
)

// Lock is a held single-updater lock. The OS releases it if the process dies,
// so there is no stale-lock heuristic to get wrong.
type Lock struct {
	f *os.File
}

// Acquire takes the lock for a state directory without blocking, returning
// ota.ErrLocked if another process holds it.
func Acquire(stateDir string) (*Lock, error) { return AcquireNamed(stateDir, "lock") }

// LauncherLock is held by a resident launcher for its whole life, so another
// process can tell a launcher is running without any socket or pid file.
const LauncherLock = "launcher.lock"

// Held reports whether another process currently holds the named lock.
func Held(stateDir, name string) bool {
	l, err := AcquireNamed(stateDir, name)
	if err != nil {
		return errors.Is(err, ota.ErrLocked)
	}
	l.Release()
	return false
}

// AcquireNamed takes a named lock in a state directory without blocking.
func AcquireNamed(stateDir, name string) (*Lock, error) {
	if err := EnsureDir(stateDir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, name), os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- lock file under the agent's own state dir
	if err != nil {
		return nil, fmt.Errorf("stage: open lock: %w", err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. Safe to call more than once.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlockFile(l.f)
	_ = l.f.Close()
	l.f = nil
}

// EnsureDir creates the state directory owner-only. It holds partial
// downloads of the program and the record of which version is trusted, so
// nobody else on the machine gets to read or plant files in it.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("stage: create %s: %w", dir, err)
	}
	return os.Chmod(dir, 0o700) // #nosec G302 -- a directory needs the execute bit; owner-only is the point
}

var errLocked = ota.ErrLocked

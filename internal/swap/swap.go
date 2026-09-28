// Package swap replaces the installed binary with a verified new one and puts
// it back when the new one fails. Every step is ordered so that a crash at any
// point leaves a state Repair can resolve to exactly one complete binary.
package swap

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ArmornetIO/ota/internal/stage"
	"github.com/ArmornetIO/ota/internal/testkit"
)

// Layout locates everything ota keeps for one installed binary. State lives in
// <dir>/.ota/<binary>/ so it shares the binary's filesystem (renames between
// them are atomic) and two programs installed side by side never share a lock.
type Layout struct {
	Binary   string
	StateDir string
}

// For returns the layout for an installed binary path.
func For(binary string) Layout {
	return Layout{
		Binary:   binary,
		StateDir: filepath.Join(filepath.Dir(binary), ".ota", filepath.Base(binary)),
	}
}

func (l Layout) statePath() string   { return filepath.Join(l.StateDir, "state.json") }
func (l Layout) pendingPath() string { return filepath.Join(l.StateDir, "pending.json") }

// Prev is where the previous binary waits until the new one commits.
func (l Layout) Prev() string { return filepath.Join(l.StateDir, "prev") }

// State is what persists between updates.
type State struct {
	// LastOfferIssuedAt is the replay floor: an offer not newer than this is refused.
	LastOfferIssuedAt     int64  `json:"last_offer_issued_at"`
	RunningVersion        string `json:"running_version,omitempty"`
	DeferredUntil         int64  `json:"deferred_until,omitempty"`
	DeferredOfferIssuedAt int64  `json:"deferred_offer_issued_at,omitempty"`
	TrustRootVersion      int64  `json:"trust_root_version,omitempty"`
}

// Pending is written before the swap and removed only once the attempt has
// committed or rolled back, so a restart always finds an unfinished attempt.
type Pending struct {
	Version       string `json:"version"`
	PrevVersion   string `json:"prev_version"`
	OfferIssuedAt int64  `json:"offer_issued_at"`
	SHA256        string `json:"sha256"`
	// Deadline is when an uncommitted version is rolled back, in Unix ms.
	// Zero means the new version has not started yet: an update staged for the
	// next start must not expire while nothing is running it, so the clock
	// starts at its first Boot.
	Deadline int64 `json:"deadline"`
	// Attempt counts starts of the new version; a crash loop exhausts it.
	Attempt int `json:"attempt"`
}

// Expired reports whether the commit deadline has passed.
func (p Pending) Expired(now time.Time) bool { return p.Deadline != 0 && now.UnixMilli() >= p.Deadline }

// For returns the layout for an installed binary whose state lives in an
// explicit directory, for layouts where the binary's own directory changes
// between versions (side-by-side installs).
func ForState(binary, stateDir string) Layout {
	return Layout{Binary: binary, StateDir: stateDir}
}

// LoadState returns the persisted state, or the zero State if none exists yet.
func LoadState(l Layout) (State, error) {
	var s State
	if err := stage.ReadJSON(l.statePath(), &s); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return State{}, err
	}
	return s, nil
}

// SaveState persists s atomically.
func SaveState(l Layout, s State) error { return stage.WriteJSON(l.statePath(), s) }

// LoadPending returns the unfinished attempt, if any.
func LoadPending(l Layout) (*Pending, error) {
	var p Pending
	if err := stage.ReadJSON(l.pendingPath(), &p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// SavePending persists p atomically.
func SavePending(l Layout, p Pending) error { return stage.WriteJSON(l.pendingPath(), p) }

// Install swaps newPath in as the binary. The pending record is written first,
// so a crash anywhere after this call starts is an attempt Repair or the
// health gate will finish.
func Install(l Layout, newPath string, p Pending) error {
	fi, err := os.Stat(l.Binary)
	if err != nil {
		return fmt.Errorf("swap: stat installed binary: %w", err)
	}
	// The download is 0600 while it is untrusted; the installed file takes the
	// current binary's mode so its owner and exec bits do not change.
	if err := os.Chmod(newPath, fi.Mode().Perm()); err != nil {
		return fmt.Errorf("swap: chmod: %w", err)
	}
	if err := SavePending(l, p); err != nil {
		return fmt.Errorf("swap: write pending: %w", err)
	}
	testkit.KillAt("after-pending")
	if err := stage.Remove(l.Prev()); err != nil {
		return err
	}
	if err := install(l, newPath); err != nil {
		return err
	}
	testkit.KillAt("after-swap")
	return syncBoth(l)
}

// Restore puts the previous binary back and ends the attempt.
func Restore(l Layout) error {
	if _, err := os.Stat(l.Prev()); err != nil {
		return fmt.Errorf("swap: no previous binary to restore: %w", err)
	}
	if err := restore(l); err != nil {
		return err
	}
	if err := syncBoth(l); err != nil {
		return err
	}
	return stage.Remove(l.pendingPath())
}

// Commit ends a successful attempt. The previous binary is removed; on Windows
// that can fail while an old process still runs from it, and the sweep at the
// next start finishes the job.
func Commit(l Layout) error {
	if err := stage.Remove(l.pendingPath()); err != nil {
		return err
	}
	_ = stage.Remove(l.Prev())
	return nil
}

// Repair resolves a swap interrupted between its two renames, where no binary
// exists at the install path. It restores the previous one and reports whether
// it had to.
func Repair(l Layout) (bool, error) {
	if _, err := os.Stat(l.Binary); err == nil {
		return false, nil
	}
	if _, err := os.Stat(l.Prev()); err != nil {
		return false, nil
	}
	if err := os.Rename(l.Prev(), l.Binary); err != nil {
		return false, fmt.Errorf("swap: repair: %w", err)
	}
	if err := syncBoth(l); err != nil {
		return true, err
	}
	return true, stage.Remove(l.pendingPath())
}

// Clean removes the previous binary when no attempt is pending, and any
// displaced binaries left by a Windows rollback.
func Clean(l Layout) {
	if p, err := LoadPending(l); err == nil && p == nil {
		_ = stage.Remove(l.Prev())
	}
	matches, _ := filepath.Glob(filepath.Join(l.StateDir, "bad-*"))
	for _, m := range matches {
		_ = stage.Remove(m)
	}
}

func syncBoth(l Layout) error {
	if err := stage.SyncDir(filepath.Dir(l.Binary)); err != nil {
		return err
	}
	return stage.SyncDir(l.StateDir)
}

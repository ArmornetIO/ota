package stage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// PartPrefix marks every temporary file this package creates, so a sweep can
// tell ours from anything else in the directory.
const PartPrefix = ".ota-"

// CreateTemp makes a new, empty temp file owner-only. O_EXCL means a file
// planted at a predictable name can never be opened and written through.
func CreateTemp(dir, suffix string) (*os.File, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	name := filepath.Join(dir, PartPrefix+hex.EncodeToString(b[:])+suffix)
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600) // #nosec G304 -- random name under the caller's dir; O_EXCL refuses a planted file
	if err != nil {
		return nil, fmt.Errorf("stage: create temp: %w", err)
	}
	return f, nil
}

// WriteJSON replaces path atomically: temp file, fsync, rename, fsync the
// directory. A crash leaves either the old file or the new one, never half.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := CreateTemp(dir, ".json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return SyncDir(dir)
}

// ReadJSON loads path into v. A missing file reports fs.ErrNotExist so callers
// can treat "no record" as a state, not a failure.
func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path) // #nosec G304 -- state files under the agent's own stage dir
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("stage: decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

// Remove deletes path, treating absence as success.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Sweep removes temp files from crashed runs. keep names files still in use
// (the partial named by an active download record). Call only while holding
// the lock, or a live download in another process would be deleted under it.
func Sweep(dir string, keep ...string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	kept := map[string]bool{}
	for _, k := range keep {
		kept[filepath.Base(k)] = true
	}
	var errs []error
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), PartPrefix) || kept[e.Name()] {
			continue
		}
		if err := Remove(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

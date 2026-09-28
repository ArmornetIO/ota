package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ArmornetIO/ota"
)

// ErrNotFound reports an unknown artifact.
var ErrNotFound = errors.New("server: artifact not found")

// BlobStore holds artifact bytes keyed by SHA-256.
type BlobStore interface {
	Open(ctx context.Context, sha256 string) (io.ReadSeekCloser, time.Time, error)
	// Put stores r under sha256, refusing bytes that do not hash to it.
	Put(ctx context.Context, sha256 string, r io.Reader) error
}

// FSBlobStore keeps artifacts as files named by their hash.
type FSBlobStore struct {
	Dir string
}

// Open implements BlobStore.
func (s FSBlobStore) Open(_ context.Context, sum string) (io.ReadSeekCloser, time.Time, error) {
	if !ota.ValidSHA256(sum) {
		return nil, time.Time{}, ErrNotFound
	}
	f, err := os.Open(filepath.Join(s.Dir, sum)) // #nosec G304 -- sum passed ValidSHA256 above; content-addressed name, no traversal
	if errors.Is(err, fs.ErrNotExist) {
		return nil, time.Time{}, ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, time.Time{}, err
	}
	return f, fi.ModTime(), nil
}

// Put implements BlobStore. The file only appears under its final name once
// its hash has been checked, so a reader never sees a partial or wrong blob.
func (s FSBlobStore) Put(_ context.Context, sum string, r io.Reader) error {
	if !ota.ValidSHA256(sum) {
		return fmt.Errorf("server: invalid sha256 %q", sum)
	}
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".put-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		_ = tmp.Close()
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		_ = tmp.Close()
		return fmt.Errorf("server: blob hashes to %s, not %s", got, sum)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.Dir, sum))
}

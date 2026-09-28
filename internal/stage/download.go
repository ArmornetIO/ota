package stage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ArmornetIO/ota"
)

// Margin is kept free beyond the download itself, so finishing an update never
// fills the disk the program is about to run from.
const Margin = 64 << 20

// Fetcher opens the artifact starting at offset. It returns the offset the
// body actually starts at: a source that ignores Range answers from 0, and the
// download then restarts rather than appending the wrong bytes.
type Fetcher func(ctx context.Context, offset int64) (io.ReadCloser, int64, error)

// record is download.json: the one partial file a later run may resume.
type record struct {
	SHA256 string `json:"sha256"`
	Path   string `json:"path"`
}

// Progress is called as bytes land, for events.
type Progress func(done, total int64)

// Download streams an artifact into dir and returns the path of the complete,
// verified file (mode 0600). A partial left by an interrupted run for the same
// artifact is resumed, but only after its existing bytes are re-hashed, so a
// partial altered between runs can never be laundered by the final check.
//
// On a transfer error the partial is kept for resume and ota.ErrDownload is
// returned. On a size overrun, hash mismatch or full disk the partial is
// deleted, since resuming it could only fail again.
func Download(ctx context.Context, dir string, a ota.Artifact, fetch Fetcher, progress Progress) (string, error) {
	// ParseOffer already enforces both, but this function writes to disk on the
	// strength of them and should not depend on its caller having parsed.
	if a.Size <= 0 || !ota.ValidSHA256(a.SHA256) {
		return "", fmt.Errorf("%w: artifact has no valid size or hash", ota.ErrDownload)
	}
	recPath := filepath.Join(dir, "download.json")

	f, h, offset, err := resumeOrCreate(dir, recPath, a)
	if err != nil {
		return "", err
	}
	path := f.Name()
	discard := func(cause error) (string, error) {
		_ = f.Close()
		_ = Remove(path)
		_ = Remove(recPath)
		return "", fmt.Errorf("%w: %w", ota.ErrDownload, cause)
	}

	// #nosec G115 -- a negative Size would wrap huge and fail this check closed
	if free, ok := Free(dir); ok && free < uint64(a.Size-offset)+Margin {
		return discard(fmt.Errorf("need %d bytes free in %s, have %d", uint64(a.Size-offset)+Margin, dir, free)) // #nosec G115 -- same wrap, message only
	}

	body, start, err := fetch(ctx, offset)
	if err != nil {
		_ = f.Close()
		return "", fmt.Errorf("%w: %w", ota.ErrDownload, err)
	}
	defer body.Close()
	if start != offset {
		if start != 0 {
			return discard(fmt.Errorf("source resumed at %d, asked for %d", start, offset))
		}
		if err := f.Truncate(0); err != nil {
			return discard(err)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return discard(err)
		}
		h.Reset()
		offset = 0
	}

	remaining := a.Size - offset
	w := &progressWriter{done: offset, total: a.Size, fn: progress}
	// One byte past the expected size is read on purpose: it is how an
	// oversized body is detected without trusting Content-Length.
	n, err := io.Copy(io.MultiWriter(f, h, w), io.LimitReader(body, remaining+1))
	switch {
	case errors.Is(err, syscall.ENOSPC):
		return discard(fmt.Errorf("disk full: %w", err))
	case err != nil:
		_ = f.Sync()
		_ = f.Close()
		return "", fmt.Errorf("%w: transfer interrupted after %d bytes: %w", ota.ErrDownload, offset+n, err)
	case n > remaining:
		return discard(fmt.Errorf("source sent more than the %d bytes the offer declares", a.Size))
	case n < remaining:
		_ = f.Sync()
		_ = f.Close()
		return "", fmt.Errorf("%w: transfer ended at %d of %d bytes", ota.ErrDownload, offset+n, a.Size)
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return discard(fmt.Errorf("sha256 %s does not match the offer's %s", got, a.SHA256))
	}
	if err := f.Sync(); err != nil {
		return discard(err)
	}
	if err := f.Close(); err != nil {
		return discard(err)
	}
	_ = Remove(recPath)
	return path, nil
}

// resumeOrCreate returns the file to write to, a hash already fed with any
// bytes it holds, and the offset to continue from.
func resumeOrCreate(dir, recPath string, a ota.Artifact) (*os.File, hash.Hash, int64, error) {
	if err := EnsureDir(dir); err != nil {
		return nil, nil, 0, err
	}
	var rec record
	err := ReadJSON(recPath, &rec)
	if err == nil && rec.SHA256 == a.SHA256 && filepath.Dir(rec.Path) == filepath.Clean(dir) {
		if f, h, n, ok := reopen(rec.Path, a.Size); ok {
			return f, h, n, nil
		}
	}
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		// A record for another artifact, or one that no longer resumes: its
		// partial is abandoned and removed rather than left for the sweep.
		if rec.Path != "" && filepath.Dir(rec.Path) == filepath.Clean(dir) {
			_ = Remove(rec.Path)
		}
		_ = Remove(recPath)
	}

	f, err := CreateTemp(dir, ".part")
	if err != nil {
		return nil, nil, 0, err
	}
	if err := WriteJSON(recPath, record{SHA256: a.SHA256, Path: f.Name()}); err != nil {
		_ = f.Close()
		_ = Remove(f.Name())
		return nil, nil, 0, err
	}
	return f, sha256.New(), 0, nil
}

func reopen(path string, size int64) (*os.File, hash.Hash, int64, bool) {
	f, err := os.OpenFile(path, os.O_RDWR, 0) // #nosec G304 -- our own part-file, created by CreateTemp under the stage dir
	if err != nil {
		return nil, nil, 0, false
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil || n > size {
		_ = f.Close()
		return nil, nil, 0, false
	}
	return f, h, n, true
}

// Partial returns the partial file named by an active download record, so the
// sweep keeps it.
func Partial(dir string) string {
	var rec record
	if ReadJSON(filepath.Join(dir, "download.json"), &rec) != nil {
		return ""
	}
	return rec.Path
}

type progressWriter struct {
	done, total int64
	fn          Progress
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.fn != nil {
		p.fn(p.done, p.total)
	}
	return len(b), nil
}

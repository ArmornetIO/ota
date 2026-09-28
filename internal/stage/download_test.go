package stage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ArmornetIO/ota"
)

func artifactFor(data []byte) ota.Artifact {
	sum := sha256.Sum256(data)
	return ota.Artifact{OS: "linux", Arch: "amd64", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

// source serves data from offset, optionally cutting off after limit bytes
// and optionally ignoring Range.
type source struct {
	data        []byte
	limit       int64
	ignoreRange bool
	offsets     []int64
}

func (s *source) fetch(_ context.Context, offset int64) (io.ReadCloser, int64, error) {
	s.offsets = append(s.offsets, offset)
	if s.ignoreRange {
		offset = 0
	}
	body := s.data[offset:]
	var r io.Reader = bytes.NewReader(body)
	if s.limit > 0 {
		r = io.MultiReader(io.LimitReader(bytes.NewReader(body), s.limit), errReader{})
	}
	return io.NopCloser(r), offset, nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func TestDownloadComplete(t *testing.T) {
	dir := t.TempDir()
	data := payload(100_000)
	path, err := Download(context.Background(), dir, artifactFor(data), (&source{data: data}).fetch, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "download.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("download record left behind after success")
	}
}

func TestDownloadResumesFromPartial(t *testing.T) {
	dir := t.TempDir()
	data := payload(100_000)
	a := artifactFor(data)

	cut := &source{data: data, limit: 60_000}
	if _, err := Download(context.Background(), dir, a, cut.fetch, nil); !errors.Is(err, ota.ErrDownload) {
		t.Fatalf("interrupted download err = %v", err)
	}
	if Partial(dir) == "" {
		t.Fatal("partial not kept for resume")
	}

	full := &source{data: data}
	path, err := Download(context.Background(), dir, a, full.fetch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if full.offsets[0] != 60_000 {
		t.Fatalf("resumed from %d, want 60000", full.offsets[0])
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, data) {
		t.Fatal("resumed content differs")
	}
}

// A partial altered between runs must not be trusted: the resumed tail would
// hash correctly on its own, so only re-hashing the prefix catches it.
func TestDownloadTamperedPartialFailsAndIsDiscarded(t *testing.T) {
	dir := t.TempDir()
	data := payload(100_000)
	a := artifactFor(data)
	_, _ = Download(context.Background(), dir, a, (&source{data: data, limit: 50_000}).fetch, nil)

	p := Partial(dir)
	b, _ := os.ReadFile(p)
	b[10] ^= 0xff
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Download(context.Background(), dir, a, (&source{data: data}).fetch, nil); !errors.Is(err, ota.ErrDownload) {
		t.Fatalf("tampered resume err = %v, want ErrDownload", err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("tampered partial kept")
	}
	path, err := Download(context.Background(), dir, a, (&source{data: data}).fetch, nil)
	if err != nil {
		t.Fatalf("fresh download after discard: %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
}

func TestDownloadSourceIgnoringRangeRestarts(t *testing.T) {
	dir := t.TempDir()
	data := payload(50_000)
	a := artifactFor(data)
	_, _ = Download(context.Background(), dir, a, (&source{data: data, limit: 20_000}).fetch, nil)
	path, err := Download(context.Background(), dir, a, (&source{data: data, ignoreRange: true}).fetch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, data) {
		t.Fatal("content differs after range-ignoring source")
	}
}

func TestDownloadOverrunAndMismatch(t *testing.T) {
	data := payload(10_000)

	dir := t.TempDir()
	a := artifactFor(data)
	a.Size = 5_000
	if _, err := Download(context.Background(), dir, a, (&source{data: data}).fetch, nil); !errors.Is(err, ota.ErrDownload) {
		t.Fatalf("overrun err = %v", err)
	}
	assertNoParts(t, dir)

	dir = t.TempDir()
	a = artifactFor(data)
	a.SHA256 = hex.EncodeToString(make([]byte, 32))
	if _, err := Download(context.Background(), dir, a, (&source{data: data}).fetch, nil); !errors.Is(err, ota.ErrDownload) {
		t.Fatalf("mismatch err = %v", err)
	}
	assertNoParts(t, dir)
}

func assertNoParts(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".part" {
			t.Fatalf("partial %s left behind", e.Name())
		}
	}
}

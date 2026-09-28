package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, blob []byte) (*httptest.Server, string) {
	t.Helper()
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])
	blobs := FSBlobStore{Dir: t.TempDir()}
	if err := blobs.Put(context.Background(), sha, bytes.NewReader(blob)); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Store: NewMemStore(), Blobs: blobs})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return hs, sha
}

func get(t *testing.T, url string, h map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	// A bare transport, so gzip is neither requested nor decoded behind our back.
	resp, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestArtifactRangeAndGzip(t *testing.T) {
	blob := bytes.Repeat([]byte("ota-artifact "), 1000)
	hs, sha := newTestServer(t, blob)
	url := hs.URL + "/v1/artifacts/" + sha

	resp := get(t, url, map[string]string{"Range": "bytes=100-"})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, blob[100:]) {
		t.Fatalf("range: status %d, %d bytes", resp.StatusCode, len(body))
	}
	if resp.Header.Get("ETag") != `"`+sha+`"` {
		t.Fatalf("ETag = %q", resp.Header.Get("ETag"))
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("ranged response was compressed")
	}

	resp = get(t, url, map[string]string{"Accept-Encoding": "gzip"})
	zr, err := gzip.NewReader(resp.Body)
	if err != nil || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("whole-file gzip not served: %v", err)
	}
	body, _ = io.ReadAll(zr)
	resp.Body.Close()
	if !bytes.Equal(body, blob) {
		t.Fatal("gzip body differs")
	}
}

func TestArtifactPathValidated(t *testing.T) {
	hs, _ := newTestServer(t, []byte("x"))
	for _, p := range []string{"../../etc/passwd", strings.Repeat("A", 64), "abc"} {
		resp := get(t, hs.URL+"/v1/artifacts/"+p, nil)
		resp.Body.Close()
		// ServeMux answers a dot-dot path with a redirect to the cleaned path,
		// which is outside /v1/artifacts/; either way nothing is served.
		// The redirect code varies by Go version (301 before 1.26, 307 after).
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode/100 != 3 {
			t.Errorf("%q: status %d, want 404 or a redirect away", p, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/v1/artifacts/") {
			t.Errorf("%q: redirected within the artifact tree to %q", p, loc)
		}
	}
}

func TestBlobPutRejectsWrongHash(t *testing.T) {
	blobs := FSBlobStore{Dir: t.TempDir()}
	if err := blobs.Put(context.Background(), strings.Repeat("a", 64), strings.NewReader("x")); err == nil {
		t.Fatal("blob stored under a hash it does not have")
	}
}

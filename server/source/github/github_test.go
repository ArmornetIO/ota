package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReleasesSkipsDraftsAndPrereleasesAndSendsToken(t *testing.T) {
	var sawAuth, sawAccept string
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/o/r/releases", func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Write([]byte(`[
		  {"tag_name":"v2","draft":true,"assets":[]},
		  {"tag_name":"v1.9.0-rc.1","prerelease":true,"assets":[]},
		  {"tag_name":"v1.8.0","assets":[{"name":"ota-offer.json","size":3,"url":"` + base + `/assets/1"}]}
		]`))
	})
	mux.HandleFunc("/assets/1", func(w http.ResponseWriter, r *http.Request) {
		sawAccept = r.Header.Get("Accept")
		w.Write([]byte("abc"))
	})
	hs := httptest.NewServer(mux)
	defer hs.Close()
	base = hs.URL

	s, err := New("o/r", "tok")
	if err != nil {
		t.Fatal(err)
	}
	s.APIURL = hs.URL
	rels, err := s.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].Tag != "v1.8.0" {
		t.Fatalf("releases = %+v", rels)
	}
	rc, err := rels[0].Assets[0].Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "abc" || sawAuth != "Bearer tok" || sawAccept != "application/octet-stream" {
		t.Fatalf("body %q auth %q accept %q", b, sawAuth, sawAccept)
	}
}

func TestNewRejectsBadRepo(t *testing.T) {
	for _, r := range []string{"", "o", "o/r/x", "../etc", "o/..", "o/.", "o/r?x=1", "o.x/r"} {
		if _, err := New(r, ""); err == nil {
			t.Errorf("repo %q accepted", r)
		}
	}
}

package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/google/go-github/v93/github"
)

func TestReleasesSkipsDraftsAndPrereleasesAndSendsToken(t *testing.T) {
	var sawAuth, sawAssetAuth, sawAccept string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases", func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Write([]byte(`[
		  {"tag_name":"v2","draft":true,"assets":[]},
		  {"tag_name":"v1.9.0-rc.1","prerelease":true,"assets":[]},
		  {"tag_name":"v1.8.0","assets":[{"id":1,"name":"ota-offer.json","size":3}]}
		]`))
	})
	mux.HandleFunc("/repos/o/r/releases/assets/1", func(w http.ResponseWriter, r *http.Request) {
		sawAssetAuth = r.Header.Get("Authorization")
		sawAccept = r.Header.Get("Accept")
		w.Write([]byte("abc"))
	})
	hs := httptest.NewServer(mux)
	defer hs.Close()

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
	if string(b) != "abc" || sawAuth != "Bearer tok" || sawAssetAuth != "Bearer tok" || sawAccept != "application/octet-stream" {
		t.Fatalf("body %q auth %q asset auth %q accept %q", b, sawAuth, sawAssetAuth, sawAccept)
	}
}

// GitHub answers an asset download with a redirect to signed storage; the
// token is for GitHub alone.
func TestAssetRedirectDoesNotCarryToken(t *testing.T) {
	var storageAuth = "unset"
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storageAuth = r.Header.Get("Authorization")
		w.Write([]byte("abc"))
	}))
	defer storage.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"tag_name":"v1","assets":[{"id":7,"name":"a","size":3}]}]`))
	})
	mux.HandleFunc("/repos/o/r/releases/assets/7", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, storage.URL+"/blob", http.StatusFound)
	})
	hs := httptest.NewServer(mux)
	defer hs.Close()

	s, _ := New("o/r", "tok")
	s.APIURL = hs.URL
	rels, err := s.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rc, err := rels[0].Assets[0].Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "abc" || storageAuth != "" {
		t.Fatalf("body %q storage auth %q", b, storageAuth)
	}
}

func TestReleasesErrorDoesNotLeakToken(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	defer hs.Close()
	s, _ := New("o/r", "sekrit")
	s.APIURL = hs.URL
	_, err := s.Releases(context.Background())
	if err == nil {
		t.Fatal("401 accepted")
	}
	var er *gh.ErrorResponse
	if !errors.As(err, &er) || er.Response.Request.Header.Get("Authorization") != "" {
		t.Fatalf("err %v keeps the token on its request", err)
	}
}

func TestNewRejectsBadRepo(t *testing.T) {
	for _, r := range []string{"", "o", "o/r/x", "../etc", "o/..", "o/.", "o/r?x=1", "o.x/r"} {
		if _, err := New(r, ""); err == nil {
			t.Errorf("repo %q accepted", r)
		}
	}
}

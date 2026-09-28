package httpindex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReleasesResolvesRelativeAssetURLs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rel/index.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"releases":[{"tag":"v1.8.0","assets":[{"name":"ota-offer.json","url":"v1.8.0/ota-offer.json","size":2}]}]}`))
	})
	mux.HandleFunc("/rel/v1.8.0/ota-offer.json", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("{}")) })
	hs := httptest.NewServer(mux)
	defer hs.Close()

	rels, err := (&Source{URL: hs.URL + "/rel/index.json"}).Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || len(rels[0].Assets) != 1 {
		t.Fatalf("releases = %+v", rels)
	}
	rc, err := rels[0].Assets[0].Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "{}" {
		t.Fatalf("asset = %q", b)
	}
}

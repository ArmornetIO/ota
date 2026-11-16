package client

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArmornetIO/ota"
)

func anyPlatform(o *ota.Offer) {
	o.Artifacts[0].OS, o.Artifacts[0].Arch = AnyPlatform, AnyPlatform
}

func (e *env) dataClient(state string) *Client {
	return e.client("0.0.0", func(o *Options) { o.StateDir = state })
}

func TestFetchPlacesTheArtifact(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	e.publish("1.0.0", time.Unix(9_000, 0), anyPlatform)

	f, err := e.dataClient(state).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Path)
	if err != nil || string(b) != string(e.newBin) || f.Version != "1.0.0" || len(f.SHA256) != 64 {
		t.Fatalf("fetched %+v: %q, %v", f, b, err)
	}
	if e.installed() != "v1" {
		t.Fatal("a data fetch touched the binary")
	}

	// With nothing newer to offer, Fetch returns what is on disk. (The same
	// offer re-sent is a replay and refused, exactly as for a binary.)
	e.tr.mu.Lock()
	e.tr.offer = nil
	e.tr.mu.Unlock()
	again, err := e.dataClient(state).Fetch(context.Background())
	if err != nil || again != f {
		t.Fatalf("second fetch: %+v, %v; want %+v", again, err, f)
	}
	if s := e.tr.stages(); s[len(s)-1] != ota.StageCommitted {
		t.Fatalf("stages %v, want the fetch reported as committed", s)
	}
}

func TestFetchKeepsPreviousOnFailure(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	e.publish("1.0.0", time.Unix(9_000, 0), anyPlatform)
	first, err := e.dataClient(state).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("bad signature", func(t *testing.T) {
		e.publish("1.1.0", time.Unix(9_100, 0), anyPlatform)
		e.tr.offer.Signature[0] ^= 1
		if _, err := e.dataClient(state).Fetch(context.Background()); !errors.Is(err, ota.ErrRefused) {
			t.Fatalf("err = %v", err)
		}
		assertOnly(t, state, first)
	})
	t.Run("hash mismatch", func(t *testing.T) {
		e.publish("1.1.0", time.Unix(9_200, 0), anyPlatform)
		e.tr.blob = []byte(strings.Repeat("x", len(e.tr.blob)))
		if _, err := e.dataClient(state).Fetch(context.Background()); err == nil {
			t.Fatal("a corrupted artifact was accepted")
		}
		assertOnly(t, state, first)
	})
	t.Run("no any/any artifact", func(t *testing.T) {
		e.publish("1.1.0", time.Unix(9_300, 0), nil)
		_, err := e.dataClient(state).Fetch(context.Background())
		if err == nil || !strings.Contains(err.Error(), "any/any") {
			t.Fatalf("err = %v", err)
		}
		assertOnly(t, state, first)
	})
}

func TestFetchRefusesReplay(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	e.publish("1.1.0", time.Unix(9_000, 0), anyPlatform)
	if _, err := e.dataClient(state).Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.publish("1.2.0", time.Unix(8_000, 0), anyPlatform) // signed, newer version, older offer
	if _, err := e.dataClient(state).Fetch(context.Background()); !errors.Is(err, ota.ErrRefused) {
		t.Fatalf("an older offer was accepted: %v", err)
	}
}

func TestFetchPrunesToTwoVersions(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	for i, v := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		e.publish(v, time.Unix(int64(9_000+i), 0), anyPlatform)
		if _, err := e.dataClient(state).Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	es, _ := os.ReadDir(filepath.Join(state, dataDir))
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "1.1.0,1.2.0" {
		t.Fatalf("data dir holds %v", names)
	}
}

// assertOnly checks that the only version on disk is want, intact.
func assertOnly(t *testing.T, state string, want Fetched) {
	t.Helper()
	es, _ := os.ReadDir(filepath.Join(state, dataDir))
	if len(es) != 1 || es[0].Name() != want.Version {
		t.Fatalf("data dir holds %v, want only %s", es, want.Version)
	}
	if _, err := os.Stat(want.Path); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentReadsWithoutTheNetwork(t *testing.T) {
	state := t.TempDir()
	if _, err := Current(state); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("nothing fetched: %v", err)
	}
	e := newEnv(t)
	e.publish("1.0.0", time.Unix(9_000, 0), anyPlatform)
	f, err := e.dataClient(state).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Current(state); err != nil || got != f {
		t.Fatalf("Current = %+v, %v; want %+v", got, err, f)
	}
}

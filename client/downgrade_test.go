package client

import (
	"context"
	stded "crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/verify/ed25519"
)

// commitVersion drives one full update to version and commits it.
func commitVersion(t *testing.T, e *env, from, to string, issued time.Time, mod func(*ota.Offer)) {
	t.Helper()
	e.publish(to, issued, mod)
	if _, err := e.client(from, nil).CheckAndUpdate(context.Background()); err != nil {
		t.Fatalf("update %s→%s: %v", from, to, err)
	}
	c := e.client(to, func(o *Options) { o.CommitDeadline = time.Hour })
	br, err := c.Boot(context.Background())
	if err != nil || br.Gate == nil {
		t.Fatalf("boot %s: %v", to, err)
	}
	c.Commit()
	if r := <-br.Gate; r.Stage != ota.StageCommitted {
		t.Fatalf("commit %s: %+v", to, r)
	}
}

// US8: a downgrade happens only through a new publisher-signed offer marked
// downgrade; replays and unmarked or unsigned downgrades are refused.
func TestDeliberateDowngrade(t *testing.T) {
	e := newEnv(t)
	commitVersion(t, e, "1.7.0", "1.8.0", e.now.Add(-time.Hour), nil)
	captured := e.tr.offer // the 1.8.0 offer, for a replay attempt later

	// An unmarked older version is refused.
	e.publish("1.7.0", e.now.Add(-time.Minute), nil)
	if _, err := e.client("1.8.0", nil).CheckAndUpdate(context.Background()); !errors.Is(err, ota.ErrRefused) {
		t.Fatalf("unmarked downgrade: %v", err)
	}
	// A server-fabricated rollback (signed by someone else) is refused.
	_, rogue, _ := stded.GenerateKey(nil)
	e.publish("1.7.0", e.now, func(o *ota.Offer) { o.Downgrade = true })
	e.tr.offer.Signature = ed25519.Sign(rogue, e.tr.offer.Offer)
	if _, err := e.client("1.8.0", nil).CheckAndUpdate(context.Background()); !errors.Is(err, ota.ErrRefused) {
		t.Fatalf("unsigned downgrade: %v", err)
	}

	// The publisher signs a rollback offer: it installs through the normal path.
	commitVersion(t, e, "1.8.0", "1.7.0", e.now, func(o *ota.Offer) { o.Downgrade = true })
	if e.installed() != "binary 1.7.0" {
		t.Fatalf("installed %q", e.installed())
	}

	// Replaying the earlier 1.8.0 offer cannot move the machine forward again
	// against the publisher's rollback: it is older than the floor.
	e.tr.offer = captured
	if _, err := e.client("1.7.0", nil).CheckAndUpdate(context.Background()); !errors.Is(err, ota.ErrRefused) {
		t.Fatalf("replayed offer: %v", err)
	}
}

// A signed offer can rotate the trust root; afterwards only the new
// keys are trusted, across restarts, and an older rotation cannot undo it.
func TestTrustRootRotation(t *testing.T) {
	e := newEnv(t)
	newPub, newPriv, _ := stded.GenerateKey(nil)
	root, _ := json.Marshal(ed25519.Root{Keys: []string{base64.StdEncoding.EncodeToString(newPub)}})

	commitVersion(t, e, "1.0.0", "1.1.0", e.now.Add(-time.Hour), func(o *ota.Offer) {
		o.TrustRootUpdate = &ota.TrustRootUpdate{Version: 2, Kind: ed25519.Kind, Root: root}
	})

	// The old key no longer works, even in a fresh client (a restart).
	e.publish("1.2.0", e.now, nil)
	if _, err := e.client("1.1.0", nil).CheckAndUpdate(context.Background()); !errors.Is(err, ota.ErrRefused) {
		t.Fatalf("offer under the retired key: %v", err)
	}
	// The new key does.
	e.key = newPriv
	e.publish("1.2.0", e.now.Add(time.Second), nil)
	if _, err := e.client("1.1.0", nil).CheckAndUpdate(context.Background()); err != nil {
		t.Fatalf("offer under the new key: %v", err)
	}
}

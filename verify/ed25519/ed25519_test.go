package ed25519

import (
	"context"
	stded "crypto/ed25519"
	"testing"
)

func keypair(t *testing.T) (stded.PublicKey, stded.PrivateKey) {
	t.Helper()
	pub, priv, err := stded.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestVerify(t *testing.T) {
	pub, priv := keypair(t)
	other, _ := keypair(t)
	payload := []byte(`{"schema":"ota.offer/v1"}`)
	sig := Sign(priv, payload)

	v, err := New(other, pub)
	if err != nil {
		t.Fatal(err)
	}
	id, err := v.Verify(context.Background(), payload, append(sig, '\n'))
	if err != nil {
		t.Fatalf("good signature rejected: %v", err)
	}
	if id.Subject != Fingerprint(pub) {
		t.Fatalf("identity = %+v", id)
	}

	flipped := append([]byte(nil), payload...)
	flipped[3] ^= 1
	if _, err := v.Verify(context.Background(), flipped, sig); err == nil {
		t.Fatal("signature over altered bytes accepted")
	}

	only, _ := New(other)
	if _, err := only.Verify(context.Background(), payload, sig); err == nil {
		t.Fatal("signature from an untrusted key accepted")
	}

	if _, err := v.Verify(context.Background(), payload, []byte("not base64!")); err == nil {
		t.Fatal("garbage signature accepted")
	}
}

func TestNewRequiresKeys(t *testing.T) {
	if _, err := New(); err == nil {
		t.Fatal("New with no keys succeeded")
	}
	if _, err := New(stded.PublicKey{1, 2, 3}); err == nil {
		t.Fatal("New accepted a short key")
	}
}

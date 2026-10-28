// Package ed25519 verifies offers signed with a pinned ed25519 key. It is the
// dependency-free alternative to Sigstore: the host pins the public keys, and
// rotating them means shipping the new key in a release signed by an old one.
package ed25519

import (
	"bytes"
	"context"
	stded "crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ArmornetIO/ota/verify"
)

// Kind is the signature format name used on the wire.
const Kind = "ed25519"

// Verifier accepts a signature from any one of its keys.
type Verifier struct {
	keys []stded.PublicKey
}

// New returns a Verifier trusting these keys. At least one is required, so a
// misconfigured host fails at startup rather than accepting nothing silently
// or, worse, being mistaken for one that verifies.
func New(keys ...stded.PublicKey) (*Verifier, error) {
	if len(keys) == 0 {
		return nil, errors.New("ed25519: no keys")
	}
	for i, k := range keys {
		if len(k) != stded.PublicKeySize {
			return nil, fmt.Errorf("ed25519: key %d is %d bytes, want %d", i, len(k), stded.PublicKeySize)
		}
	}
	return &Verifier{keys: append([]stded.PublicKey(nil), keys...)}, nil
}

// The client reaches Rotate by assertion, so losing it would only show as a
// refused rotation at run time.
var _ verify.Rotatable = (*Verifier)(nil)

// Kind implements verify.Verifier.
func (v *Verifier) Kind() string { return Kind }

// Verify checks a base64 signature (the content of an ota-offer.json.sig file,
// surrounding whitespace allowed) over payload.
func (v *Verifier) Verify(_ context.Context, payload, sig []byte) (verify.Identity, error) {
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sig)))
	if err != nil {
		return verify.Identity{}, fmt.Errorf("ed25519: decode signature: %w", err)
	}
	if len(raw) != stded.SignatureSize {
		return verify.Identity{}, fmt.Errorf("ed25519: signature is %d bytes, want %d", len(raw), stded.SignatureSize)
	}
	for _, k := range v.keys {
		if stded.Verify(k, payload, raw) {
			return verify.Identity{Kind: Kind, Subject: Fingerprint(k)}, nil
		}
	}
	return verify.Identity{}, errors.New("ed25519: signature does not match any trusted key")
}

// Root is the ed25519 trust-root format for rotation: the complete new set of
// trusted public keys, base64. Listing old and new keys together lets offers
// signed by either verify during the changeover.
type Root struct {
	Keys []string `json:"keys"`
}

// Rotate implements verify.Rotatable.
func (v *Verifier) Rotate(root []byte) (verify.Verifier, error) {
	var r Root
	if err := json.Unmarshal(root, &r); err != nil {
		return nil, fmt.Errorf("ed25519: trust root: %w", err)
	}
	keys := make([]stded.PublicKey, 0, len(r.Keys))
	for _, k := range r.Keys {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil {
			return nil, fmt.Errorf("ed25519: trust root key: %w", err)
		}
		keys = append(keys, stded.PublicKey(b))
	}
	return New(keys...)
}

// Sign produces the base64 signature format Verify accepts. It exists for
// tests and for publishers who sign with a local key instead of Sigstore.
func Sign(priv stded.PrivateKey, payload []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(stded.Sign(priv, payload)))
}

// Fingerprint is a short stable name for a key: the first 16 hex characters of
// its SHA-256.
func Fingerprint(k stded.PublicKey) string {
	sum := sha256.Sum256(k)
	return hex.EncodeToString(sum[:8])
}

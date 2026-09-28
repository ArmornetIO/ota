// Package verify defines how an offer's signature is checked. Implementations
// live in subpackages so a program links only the one it uses: ed25519 here in
// core, Sigstore in its own module.
package verify

import "context"

// Identity names who signed a payload, for logs and reports. It is never an
// input to a trust decision; the Verifier has already made that.
type Identity struct {
	Kind    string // "ed25519", "sigstore"
	Subject string // key fingerprint, or certificate SAN
	Issuer  string // OIDC issuer for keyless signatures
}

// Rotatable is a Verifier whose trust can be replaced by new trust material
// carried in an offer. The client only rotates with material from an offer
// that already verified under the current root, so a key compromise cannot
// be bootstrapped by an unsigned rotation.
type Rotatable interface {
	Verifier
	// Rotate returns a Verifier trusting root instead. root is the Kind's own
	// format (see each implementation).
	Rotate(root []byte) (Verifier, error)
}

// Verifier checks sig over exactly these payload bytes. It must fail closed:
// any error means the payload is not trusted, and there is no "warn" result.
type Verifier interface {
	Verify(ctx context.Context, payload, sig []byte) (Identity, error)
	// Kind names the signature format, so the server can say which it serves
	// and a client can reject a mismatch before trying to parse it.
	Kind() string
}

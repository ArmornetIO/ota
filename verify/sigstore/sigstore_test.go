package sigstore

import (
	"context"
	"net/http"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	sgverify "github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	issuer = "https://token.actions.githubusercontent.com"
	signer = "https://github.com/ArmornetIO/app/.github/workflows/release.yml@refs/tags/v1.8.0"
)

// noNetwork fails the test if verification tries to reach anything:
// verification must work offline.
type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Fatalf("verification made a network request to %s", r.URL)
	return nil, nil
}

func setup(t *testing.T, p Policy) (*ca.VirtualSigstore, *Verifier) {
	t.Helper()
	prev := http.DefaultTransport
	http.DefaultTransport = noNetwork{t}
	t.Cleanup(func() { http.DefaultTransport = prev })

	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	// The virtual sigstore issues no SCTs, which is exactly the private
	// deployment this switch exists for.
	p.NoCertificateTransparency = true
	v, err := NewWithTrustedMaterial(vs, p)
	if err != nil {
		t.Fatal(err)
	}
	return vs, v
}

// sign returns signature bytes Verify will turn back into the virtual
// sigstore's entity, standing in for bundle JSON.
func sign(t *testing.T, vs *ca.VirtualSigstore, v *Verifier, identity string, payload []byte) []byte {
	t.Helper()
	e, err := vs.Sign(identity, issuer, payload)
	if err != nil {
		t.Fatal(err)
	}
	v.parse = func([]byte) (sgverify.SignedEntity, error) { return e, nil }
	return []byte("entity")
}

func TestVerifyAcceptsTheReleaseWorkflow(t *testing.T) {
	vs, v := setup(t, Policy{Issuer: issuer, SANRegex: `^https://github\.com/ArmornetIO/app/\.github/workflows/release\.yml@refs/tags/v`})
	payload := []byte(`{"schema":"ota.offer/v1"}`)
	id, err := v.Verify(context.Background(), payload, sign(t, vs, v, signer, payload))
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != signer || id.Kind != Kind {
		t.Fatalf("identity = %+v", id)
	}
}

func TestVerifyRefusesAnotherWorkflow(t *testing.T) {
	vs, v := setup(t, Policy{Issuer: issuer, SANRegex: `^https://github\.com/ArmornetIO/app/\.github/workflows/release\.yml@refs/tags/v`})
	payload := []byte(`{"schema":"ota.offer/v1"}`)
	sig := sign(t, vs, v, "https://github.com/someone/else/.github/workflows/x.yml@refs/heads/main", payload)
	if _, err := v.Verify(context.Background(), payload, sig); err == nil {
		t.Fatal("offer signed by another workflow accepted")
	}
}

func TestVerifyRefusesOtherBytes(t *testing.T) {
	vs, v := setup(t, Policy{Issuer: issuer, SAN: signer})
	sig := sign(t, vs, v, signer, []byte("the signed offer"))
	if _, err := v.Verify(context.Background(), []byte("a different offer"), sig); err == nil {
		t.Fatal("signature accepted over different bytes")
	}
}

func TestPolicyMustPinIssuerAndSigner(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Policy{{Issuer: issuer}, {SAN: signer}, {}} {
		if _, err := NewWithTrustedMaterial(vs, p); err == nil {
			t.Errorf("policy %+v accepted", p)
		}
	}
}

func TestMalformedBundleRefused(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	v, _ := NewWithTrustedMaterial(vs, Policy{Issuer: issuer, SAN: signer})
	if _, err := v.Verify(context.Background(), []byte("x"), []byte("{not a bundle")); err == nil {
		t.Fatal("malformed bundle accepted")
	}
}

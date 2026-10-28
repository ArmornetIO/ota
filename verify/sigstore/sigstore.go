// Package sigstore verifies offers signed with Sigstore (cosign sign-blob
// --bundle, or goreleaser's signs stanza driving it), typically keyless from a
// CI workflow's OIDC identity.
//
// Verification is fully offline: the bundle carries the certificate, the
// transparency-log entry with its inclusion proof and integrated time, and the
// trusted root is pinned on the client. Nothing calls Fulcio or Rekor.
package sigstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	sgverify "github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/ArmornetIO/ota/verify"
)

// Kind is the signature format name used on the wire.
const Kind = "sigstore"

// Policy is who must have signed. Pin both the OIDC issuer and the signer:
// for a GitHub Actions release, the issuer
// https://token.actions.githubusercontent.com and a SAN matching the release
// workflow at a tag, e.g.
// ^https://github.com/OWNER/REPO/\.github/workflows/release\.yml@refs/tags/v.
// Without the SAN pin any workflow on GitHub could sign an offer.
type Policy struct {
	Issuer, IssuerRegex string
	SAN, SANRegex       string
	// NoCertificateTransparency skips the signed certificate timestamp check,
	// for a private Sigstore deployment that runs no CT log. Leave it false
	// for the public-good instance, whose certificates always carry one.
	NoCertificateTransparency bool
}

func (p Policy) identity() (sgverify.CertificateIdentity, error) {
	if p.Issuer == "" && p.IssuerRegex == "" {
		return sgverify.CertificateIdentity{}, errors.New("sigstore: policy needs an issuer")
	}
	if p.SAN == "" && p.SANRegex == "" {
		return sgverify.CertificateIdentity{}, errors.New("sigstore: policy needs a signer identity (SAN)")
	}
	return sgverify.NewShortCertificateIdentity(p.Issuer, p.IssuerRegex, p.SAN, p.SANRegex)
}

// Verifier implements verify.Verifier and verify.Rotatable.
type Verifier struct {
	v      *sgverify.Verifier
	id     sgverify.CertificateIdentity
	policy Policy
	// parse turns signature bytes into a signed entity; bundle JSON unless a
	// test replaces it.
	parse func([]byte) (sgverify.SignedEntity, error)
}

// New returns a Verifier for a trusted_root.json (the Sigstore public-good
// root, or a private deployment's).
func New(trustedRootJSON []byte, p Policy) (*Verifier, error) {
	tr, err := root.NewTrustedRootFromJSON(trustedRootJSON)
	if err != nil {
		return nil, fmt.Errorf("sigstore: trusted root: %w", err)
	}
	return NewWithTrustedMaterial(tr, p)
}

// NewWithTrustedMaterial is New for callers that already hold trust material.
func NewWithTrustedMaterial(tm root.TrustedMaterial, p Policy) (*Verifier, error) {
	id, err := p.identity()
	if err != nil {
		return nil, err
	}
	// A transparency-log entry and a verified time are required: without the
	// time, a certificate valid for ten minutes could be used forever.
	opts := []sgverify.VerifierOption{
		sgverify.WithTransparencyLog(1),
		sgverify.WithObserverTimestamps(1),
	}
	if !p.NoCertificateTransparency {
		opts = append(opts, sgverify.WithSignedCertificateTimestamps(1))
	}
	v, err := sgverify.NewVerifier(tm, opts...)
	if err != nil {
		return nil, fmt.Errorf("sigstore: %w", err)
	}
	return &Verifier{v: v, id: id, policy: p, parse: parseBundle}, nil
}

func parseBundle(b []byte) (sgverify.SignedEntity, error) {
	var bn bundle.Bundle
	if err := bn.UnmarshalJSON(b); err != nil {
		return nil, fmt.Errorf("sigstore: bundle: %w", err)
	}
	return &bn, nil
}

// The client reaches Rotate by assertion, so losing it would only show as a
// refused rotation at run time.
var _ verify.Rotatable = (*Verifier)(nil)

// Kind implements verify.Verifier.
func (v *Verifier) Kind() string { return Kind }

// Verify implements verify.Verifier: sig is a Sigstore bundle over exactly
// payload, signed by the policy's identity.
func (v *Verifier) Verify(_ context.Context, payload, sig []byte) (verify.Identity, error) {
	entity, err := v.parse(sig)
	if err != nil {
		return verify.Identity{}, err
	}
	res, err := v.v.Verify(entity, sgverify.NewPolicy(
		sgverify.WithArtifact(bytes.NewReader(payload)),
		sgverify.WithCertificateIdentity(v.id),
	))
	if err != nil {
		return verify.Identity{}, fmt.Errorf("sigstore: %w", err)
	}
	id := verify.Identity{Kind: Kind}
	if res.Signature != nil && res.Signature.Certificate != nil {
		id.Subject = res.Signature.Certificate.SubjectAlternativeName
		id.Issuer = res.Signature.Certificate.Issuer
	}
	return id, nil
}

// Rotate implements verify.Rotatable: root is a new trusted_root.json. The
// signer policy is kept; changing who may sign is a new release of the client.
func (v *Verifier) Rotate(rootJSON []byte) (verify.Verifier, error) {
	return New(rootJSON, v.policy)
}

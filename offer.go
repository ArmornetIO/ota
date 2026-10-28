// Package ota holds the types shared by the ota client, server and CLI: the
// signed offer, update events and reports, and the sentinel errors callers
// branch on.
package ota

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"time"
)

// OfferSchema is the only offer schema this version understands. The major
// version is what a client checks; unknown fields are ignored so v1 clients
// survive additive changes such as deltas.
const OfferSchema = "ota.offer/v1"

// Offer is the publisher-signed statement that clients may move to Version.
// The signature covers the exact bytes it was parsed from, never a
// re-serialisation, so there is no canonical form to get wrong.
type Offer struct {
	Schema   string `json:"schema"`
	Program  string `json:"program"`
	Version  string `json:"version"`
	IssuedAt int64  `json:"issued_at"`
	// ExpiresAt is optional and off by default; zero means the offer does not
	// expire. See the accepted freeze risk in the security model.
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// Downgrade must be set for an offer whose Version is lower than what a
	// client runs, so a mislabelled offer cannot roll a machine back by accident.
	Downgrade bool     `json:"downgrade,omitempty"`
	Channels  []string `json:"channels,omitempty"`
	// MinVersion is reserved; v1 clients ignore it.
	MinVersion      string           `json:"min_version,omitempty"`
	Artifacts       []Artifact       `json:"artifacts"`
	TrustRootUpdate *TrustRootUpdate `json:"trust_root_update,omitempty"`
	// Deltas is reserved for patch updates; v1 clients ignore it.
	Deltas json.RawMessage `json:"deltas,omitempty"`

	version Version
}

// Artifact is one platform's binary, identified by its content hash.
type Artifact struct {
	OS     string   `json:"os"`
	Arch   string   `json:"arch"`
	SHA256 string   `json:"sha256"`
	Size   int64    `json:"size"`
	URLs   []string `json:"urls,omitempty"`
}

// TrustRootUpdate carries replacement trust material. A client adopts it only
// from an offer that verified under the root it already trusts.
type TrustRootUpdate struct {
	Version int64           `json:"version"`
	Kind    string          `json:"kind"`
	Root    json.RawMessage `json:"root"`
}

var (
	programRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	channelRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	goosRe    = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
)

// ParseOffer decodes and shape-checks an offer. Call it only on bytes whose
// signature has already verified: the fields are publisher input, and nothing
// here is a substitute for that check.
func ParseOffer(b []byte) (Offer, error) {
	var o Offer
	if err := json.Unmarshal(b, &o); err != nil {
		return Offer{}, fmt.Errorf("ota: decode offer: %w", err)
	}
	if err := o.validate(); err != nil {
		return Offer{}, err
	}
	return o, nil
}

// SemVer returns the parsed Version. Valid only on an Offer from ParseOffer.
func (o Offer) SemVer() Version { return o.version }

// Issued returns IssuedAt as a time.
func (o Offer) Issued() time.Time { return time.UnixMilli(o.IssuedAt) }

// Expires returns the expiry and whether one is set.
func (o Offer) Expires() (time.Time, bool) {
	if o.ExpiresAt == 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(o.ExpiresAt), true
}

// ArtifactFor returns the artifact built for exactly this OS and architecture.
func (o Offer) ArtifactFor(goos, goarch string) (Artifact, bool) {
	for _, a := range o.Artifacts {
		if a.OS == goos && a.Arch == goarch {
			return a, true
		}
	}
	return Artifact{}, false
}

// AdmitsChannel reports whether a client on channel may be shown this offer.
func (o Offer) AdmitsChannel(channel string) bool {
	return len(o.Channels) == 0 || slices.Contains(o.Channels, channel)
}

func (o *Offer) validate() error {
	if o.Schema != OfferSchema {
		return fmt.Errorf("ota: unsupported offer schema %q", o.Schema)
	}
	if !programRe.MatchString(o.Program) {
		return fmt.Errorf("ota: invalid program %q", o.Program)
	}
	v, err := ParseVersion(o.Version)
	if err != nil {
		return err
	}
	o.version = v
	if o.IssuedAt <= 0 {
		return errors.New("ota: offer has no issued_at")
	}
	if o.ExpiresAt != 0 && o.ExpiresAt <= o.IssuedAt {
		return errors.New("ota: offer expires before it was issued")
	}
	for _, c := range o.Channels {
		if !channelRe.MatchString(c) {
			return fmt.Errorf("ota: invalid channel %q", c)
		}
	}
	if len(o.Artifacts) == 0 {
		return errors.New("ota: offer has no artifacts")
	}
	seen := map[string]bool{}
	for _, a := range o.Artifacts {
		if err := a.validate(); err != nil {
			return err
		}
		key := a.OS + "/" + a.Arch
		if seen[key] {
			return fmt.Errorf("ota: offer has two artifacts for %s", key)
		}
		seen[key] = true
	}
	return nil
}

func (a Artifact) validate() error {
	if !goosRe.MatchString(a.OS) || !goosRe.MatchString(a.Arch) {
		return fmt.Errorf("ota: invalid artifact platform %q/%q", a.OS, a.Arch)
	}
	if !sha256Re.MatchString(a.SHA256) {
		return fmt.Errorf("ota: invalid artifact sha256 %q", a.SHA256)
	}
	if a.Size <= 0 {
		return fmt.Errorf("ota: artifact %s/%s has no size", a.OS, a.Arch)
	}
	for _, raw := range a.URLs {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return fmt.Errorf("ota: invalid artifact url %q", raw)
		}
		// Mirrors are fetched without the release server's authentication, so a
		// plain-http mirror would hand the bytes to anyone on the path. The hash
		// still protects integrity; this keeps the download private.
		if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
			return fmt.Errorf("ota: artifact url %q is not https", raw)
		}
	}
	return nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// ValidSHA256 reports whether s is a lowercase hex SHA-256, the only form an
// artifact path segment may take before it reaches a store.
func ValidSHA256(s string) bool { return sha256Re.MatchString(s) }

// ValidChannel reports whether s is a well-formed channel name.
func ValidChannel(s string) bool { return channelRe.MatchString(s) }

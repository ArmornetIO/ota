package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ArmornetIO/ota"
)

// Source lists published releases, for example a project's GitHub Releases.
// Implementations live in server/source/*.
type Source interface {
	Name() string
	Releases(ctx context.Context) ([]SourceRelease, error)
}

// SourceRelease is one tagged release and its downloadable files.
type SourceRelease struct {
	Tag    string
	Assets []Asset
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name string
	Size int64
	Open func(ctx context.Context) (io.ReadCloser, error)
}

// OfferAssetPrefix names the offer files in a release. A release may carry
// several (the original and later rollback offers), each with its signature
// beside it as <name>.sigstore.json or <name>.sig.
const OfferAssetPrefix = "ota-offer"

// IngestResult summarises one ingest pass.
type IngestResult struct {
	Ingested int      `json:"ingested"`
	Rejected int      `json:"rejected"`
	Known    int      `json:"known"`
	Errors   []string `json:"errors,omitempty"`
}

// maxOfferBytes bounds what is read before a signature has been checked.
const maxOfferBytes = 1 << 20

// Ingest pulls every release from the Source and stores each offer that
// verifies, with its artifacts copied into Blobs after their hash is checked.
// An offer that fails is recorded as rejected, with the reason, and never
// served.
func (s *Server) Ingest(ctx context.Context) (IngestResult, error) {
	var res IngestResult
	if s.opts.Source == nil {
		return res, errors.New("server: no release source configured")
	}
	rels, err := s.opts.Source.Releases(ctx)
	if err != nil {
		return res, fmt.Errorf("server: list releases: %w", err)
	}
	for _, rel := range rels {
		byName := map[string]Asset{}
		for _, a := range rel.Assets {
			byName[a.Name] = a
		}
		for _, a := range rel.Assets {
			if !strings.HasPrefix(a.Name, OfferAssetPrefix) || !strings.HasSuffix(a.Name, ".json") || isSignatureName(a.Name) {
				continue
			}
			so, err := s.readSignedOffer(ctx, a, byName)
			if err != nil {
				res.Rejected++
				res.Errors = append(res.Errors, fmt.Sprintf("%s %s: %v", rel.Tag, a.Name, err))
				s.recordRejected(ctx, so.Offer, s.opts.Source.Name(), rel.Tag, err)
				continue
			}
			if s.known(ctx, so) {
				res.Known++
				continue
			}
			fetch := func(ctx context.Context, art ota.Artifact) (io.ReadCloser, error) {
				// Match on the name the offer itself gives, which its URLs
				// carry. Size alone binds the wrong asset whenever two
				// platform archives happen to be the same length — the hash
				// check then rejects a perfectly good release, naming neither
				// the collision nor the asset it actually read.
				for _, u := range art.URLs {
					name := u
					if i := strings.LastIndex(name, "/"); i >= 0 {
						name = name[i+1:]
					}
					if cand, ok := byName[name]; ok && !strings.HasPrefix(cand.Name, OfferAssetPrefix) {
						return cand.Open(ctx)
					}
				}
				// Offers predating the url-template carry no usable name.
				for _, cand := range rel.Assets {
					if cand.Size == art.Size && !strings.HasPrefix(cand.Name, OfferAssetPrefix) {
						return cand.Open(ctx)
					}
				}
				return nil, errors.New("no release asset matching the offer's artifact by name or size")
			}
			if _, err := s.addOffer(ctx, so, s.opts.Source.Name(), rel.Tag, fetch); err != nil {
				res.Rejected++
				res.Errors = append(res.Errors, fmt.Sprintf("%s %s: %v", rel.Tag, a.Name, err))
				continue
			}
			res.Ingested++
		}
	}
	return res, nil
}

func isSignatureName(n string) bool {
	return strings.HasSuffix(n, ".sigstore.json") || strings.HasSuffix(n, ".sig")
}

func (s *Server) readSignedOffer(ctx context.Context, a Asset, byName map[string]Asset) (ota.SignedOffer, error) {
	so := ota.SignedOffer{}
	var err error
	if so.Offer, err = readAsset(ctx, a); err != nil {
		return so, err
	}
	if s.opts.Verifier == nil {
		return so, errors.New("no verifier configured")
	}
	so.Kind = s.opts.Verifier.Kind()
	sigName := a.Name + ".sig"
	if so.Kind == "sigstore" {
		sigName = a.Name + ".sigstore.json"
	}
	sig, ok := byName[sigName]
	if !ok {
		return so, fmt.Errorf("no %s beside the offer", sigName)
	}
	so.Signature, err = readAsset(ctx, sig)
	return so, err
}

func readAsset(ctx context.Context, a Asset) ([]byte, error) {
	rc, err := a.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxOfferBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxOfferBytes {
		return nil, errors.New("offer file is too large")
	}
	return b, nil
}

// AddOffer verifies and stores one signed offer, fetching its artifacts from
// the URLs it lists. It is the admin upload path and the rollback path: a
// publisher-signed rollback offer can be posted without touching releases.
func (s *Server) AddOffer(ctx context.Context, so ota.SignedOffer, source, tag string) (Release, error) {
	return s.addOffer(ctx, so, source, tag, nil)
}

type artifactFetcher func(ctx context.Context, a ota.Artifact) (io.ReadCloser, error)

func (s *Server) addOffer(ctx context.Context, so ota.SignedOffer, source, tag string, fetch artifactFetcher) (Release, error) {
	o, err := s.verifyOffer(ctx, so)
	if err != nil {
		s.recordRejected(ctx, so.Offer, source, tag, err)
		return Release{}, err
	}
	for _, a := range o.Artifacts {
		if err := s.storeArtifact(ctx, a, fetch); err != nil {
			err = fmt.Errorf("artifact %s/%s: %w", a.OS, a.Arch, err)
			s.recordRejected(ctx, so.Offer, source, tag, err)
			return Release{}, err
		}
	}
	rel := Release{
		Program: o.Program, Version: o.SemVer().String(), Tag: tag, Source: source,
		Status: ReleaseIngested, IngestedAt: s.now(), Offers: []StoredOffer{storedOffer(so, o)},
	}
	if err := s.opts.Store.PutRelease(ctx, rel); err != nil {
		return Release{}, err
	}
	s.log.InfoContext(ctx, "ota: offer ingested", "program", o.Program, "version", o.Version, "issued_at", o.IssuedAt, "source", source)
	return rel, nil
}

func (s *Server) verifyOffer(ctx context.Context, so ota.SignedOffer) (ota.Offer, error) {
	if s.opts.Verifier == nil {
		return ota.Offer{}, errors.New("server: no verifier configured; refusing to store an unverified offer")
	}
	if so.Kind != s.opts.Verifier.Kind() {
		return ota.Offer{}, fmt.Errorf("offer is signed as %q, this server verifies %q", so.Kind, s.opts.Verifier.Kind())
	}
	if _, err := s.opts.Verifier.Verify(ctx, so.Offer, so.Signature); err != nil {
		return ota.Offer{}, fmt.Errorf("signature: %w", err)
	}
	return ota.ParseOffer(so.Offer)
}

// storeArtifact copies an artifact into Blobs unless it is already there.
// Blobs.Put checks the hash, so a wrong mirror or asset is rejected, not stored.
func (s *Server) storeArtifact(ctx context.Context, a ota.Artifact, fetch artifactFetcher) error {
	if rc, _, err := s.opts.Blobs.Open(ctx, a.SHA256); err == nil {
		_ = rc.Close()
		return nil
	}
	var errs []error
	try := func(rc io.ReadCloser, err error) bool {
		if err != nil {
			errs = append(errs, err)
			return false
		}
		defer rc.Close()
		if err := s.opts.Blobs.Put(ctx, a.SHA256, io.LimitReader(rc, a.Size+1)); err != nil {
			errs = append(errs, err)
			return false
		}
		return true
	}
	for _, u := range a.URLs {
		if try(s.get(ctx, u)) {
			return nil
		}
	}
	if fetch != nil && try(fetch(ctx, a)) {
		return nil
	}
	if len(errs) == 0 {
		return errors.New("no URL or release asset to fetch it from")
	}
	return errors.Join(errs...)
}

func (s *Server) get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.opts.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

func (s *Server) known(ctx context.Context, so ota.SignedOffer) bool {
	o, err := ota.ParseOffer(so.Offer)
	if err != nil {
		return false
	}
	digest := storedOffer(so, o).Digest
	rels, err := s.opts.Store.Releases(ctx, o.Program)
	if err != nil {
		return false
	}
	for _, r := range rels {
		for _, so := range r.Offers {
			if so.Digest == digest && r.Status == ReleaseIngested {
				return true
			}
		}
	}
	return false
}

// recordRejected stores why an offer was refused, so the publisher can see it.
// The labels come from the unverified bytes and are for display only.
func (s *Server) recordRejected(ctx context.Context, raw []byte, source, tag string, cause error) {
	program, version := "unknown", tag
	if o, err := ota.ParseOffer(raw); err == nil {
		program, version = o.Program, o.SemVer().String()
	}
	s.log.WarnContext(ctx, "ota: offer rejected", "program", program, "version", version, "source", source, "err", cause)
	_ = s.opts.Store.PutRelease(ctx, Release{
		Program: program, Version: version, Tag: tag, Source: source,
		Status: ReleaseRejected, Reason: cause.Error(), IngestedAt: s.now(),
	})
}

// Package github reads releases from GitHub Releases (or GitHub Enterprise)
// over the REST API. It is its own module so the ota core stays free of
// third-party dependencies.
package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ArmornetIO/ota/server"
	gh "github.com/google/go-github/v93/github"
)

// repoRe follows GitHub's rules: owners are letters, digits and hyphens;
// repository names add dot and underscore. The repo is spliced into an API
// path, so "." and ".." are refused separately below.
var repoRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)

func validRepo(repo string) bool {
	if !repoRe.MatchString(repo) {
		return false
	}
	name := repo[strings.IndexByte(repo, '/')+1:]
	return name != "." && name != ".."
}

// Source lists a repository's published (non-draft) releases.
type Source struct {
	// Repo is "owner/name".
	Repo string
	// Token is optional for public repositories and required for private ones.
	Token string
	// TokenFunc, when set, supplies a token per request instead of Token, for
	// credentials that expire (e.g. GitHub App installation tokens).
	TokenFunc func(context.Context) (string, error)
	// APIURL defaults to https://api.github.com; set it for GitHub Enterprise.
	APIURL string
	// IncludePrereleases also ingests releases marked pre-release.
	IncludePrereleases bool
	// PerPage is how many recent releases are read. Default 30.
	PerPage int
	Client  *http.Client
}

// New validates repo and returns a Source.
func New(repo, token string) (*Source, error) {
	if !validRepo(repo) {
		return nil, fmt.Errorf("github: repo %q is not owner/name", repo)
	}
	return &Source{Repo: repo, Token: token}, nil
}

// Name implements server.Source.
func (s *Source) Name() string { return "github:" + s.Repo }

// maxListBytes bounds the release listing; asset bodies are left unbounded
// because an artifact is as large as the publisher made it.
const maxListBytes = 16 << 20

// Releases implements server.Source.
func (s *Source) Releases(ctx context.Context) ([]server.SourceRelease, error) {
	if !validRepo(s.Repo) {
		return nil, errors.New("github: invalid repo")
	}
	per := s.PerPage
	if per == 0 {
		per = 30
	}
	// The repo is already validated; escaping keeps that true if the pattern
	// ever widens, since go-github splices owner and name in unescaped.
	o, n, _ := strings.Cut(s.Repo, "/")
	owner, name := url.PathEscape(o), url.PathEscape(n)
	gc, err := s.client()
	if err != nil {
		return nil, err
	}
	rels, _, err := gc.Repositories.ListReleases(ctx, owner, name, &gh.ListOptions{PerPage: per})
	if err != nil {
		return nil, fmt.Errorf("github: list releases: %w", err)
	}
	var out []server.SourceRelease
	for _, r := range rels {
		if r.GetDraft() || (r.GetPrerelease() && !s.IncludePrereleases) {
			continue
		}
		sr := server.SourceRelease{Tag: r.GetTagName()}
		for _, a := range r.Assets {
			id := a.GetID()
			sr.Assets = append(sr.Assets, server.Asset{
				Name: a.GetName(), Size: int64(a.GetSize()),
				Open: func(ctx context.Context) (io.ReadCloser, error) {
					// The redirect to storage is followed by the plain client, so
					// the token goes to GitHub and never to the storage host.
					rc, _, err := gc.Repositories.DownloadReleaseAsset(ctx, owner, name, id, s.httpClient())
					if err != nil {
						return nil, fmt.Errorf("github: download asset %d: %w", id, err)
					}
					return rc, nil
				},
			})
		}
		out = append(out, sr)
	}
	return out, nil
}

func (s *Source) api() string {
	if s.APIURL != "" {
		return strings.TrimRight(s.APIURL, "/")
	}
	return "https://api.github.com"
}

func (s *Source) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s *Source) client() (*gh.Client, error) {
	hc := s.httpClient()
	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	base := s.api() + "/"
	c, err := gh.NewClient(gh.WithHTTPClient(hc), gh.WithTransport(s.authorize(next)), gh.WithURLs(&base, nil))
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	return c, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// authorize resolves the token per request so a TokenFunc credential can
// expire and refresh between calls.
func (s *Source) authorize(next http.RoundTripper) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		token := s.Token
		if s.TokenFunc != nil {
			var err error
			if token, err = s.TokenFunc(req.Context()); err != nil {
				return nil, fmt.Errorf("github: token: %w", err)
			}
		}
		out := req
		if token != "" {
			out = req.Clone(req.Context())
			out.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := next.RoundTrip(out)
		if resp != nil {
			// go-github keeps the response's request inside its errors; the
			// clone carries the token, so it never leaves this function.
			resp.Request = req
			if req.Header.Get("Accept") != "application/octet-stream" {
				resp.Body = http.MaxBytesReader(nil, resp.Body, maxListBytes)
			}
		}
		return resp, err
	}
}

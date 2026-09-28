// Package github reads releases from GitHub Releases (or GitHub Enterprise)
// over the REST API, with net/http only.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/ArmornetIO/ota/server"
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

type apiRelease struct {
	TagName    string     `json:"tag_name"`
	Draft      bool       `json:"draft"`
	Prerelease bool       `json:"prerelease"`
	Assets     []apiAsset `json:"assets"`
}

type apiAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// URL is the API asset URL; with Accept: application/octet-stream it
	// redirects to the bytes, and works for private repositories.
	URL string `json:"url"`
}

// Releases implements server.Source.
func (s *Source) Releases(ctx context.Context) ([]server.SourceRelease, error) {
	if !validRepo(s.Repo) {
		return nil, errors.New("github: invalid repo")
	}
	per := s.PerPage
	if per == 0 {
		per = 30
	}
	u := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", s.api(), s.Repo, per)
	resp, err := s.do(ctx, u, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rels []apiRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&rels); err != nil {
		return nil, fmt.Errorf("github: decode releases: %w", err)
	}
	var out []server.SourceRelease
	for _, r := range rels {
		if r.Draft || (r.Prerelease && !s.IncludePrereleases) {
			continue
		}
		sr := server.SourceRelease{Tag: r.TagName}
		for _, a := range r.Assets {
			a := a
			sr.Assets = append(sr.Assets, server.Asset{
				Name: a.Name, Size: a.Size,
				Open: func(ctx context.Context) (io.ReadCloser, error) {
					resp, err := s.do(ctx, a.URL, "application/octet-stream")
					if err != nil {
						return nil, err
					}
					return resp.Body, nil
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

func (s *Source) do(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	token := s.Token
	if s.TokenFunc != nil {
		if token, err = s.TokenFunc(ctx); err != nil {
			return nil, fmt.Errorf("github: token: %w", err)
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	c := s.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("github: GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// Package httpindex reads releases from a static JSON index, which any host
// can publish: GitLab or Gitea release pages, an S3 bucket, a plain file
// server. No vendor-specific code.
//
//	{"releases": [{"tag": "v1.8.0", "assets": [
//	  {"name": "ota-offer.json", "url": "v1.8.0/ota-offer.json", "size": 812},
//	  {"name": "ota-offer.json.sig", "url": "v1.8.0/ota-offer.json.sig"},
//	  {"name": "app_linux_amd64", "url": "v1.8.0/app_linux_amd64", "size": 48213504}
//	]}]}
//
// Asset URLs may be relative to the index URL.
package httpindex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/ArmornetIO/ota/server"
)

// Source reads the index at URL.
type Source struct {
	URL    string
	Client *http.Client
	// Authorize, if set, decorates every request.
	Authorize func(*http.Request)
}

type index struct {
	Releases []struct {
		Tag    string `json:"tag"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	} `json:"releases"`
}

// Name implements server.Source.
func (s *Source) Name() string { return "index:" + s.URL }

// Releases implements server.Source.
func (s *Source) Releases(ctx context.Context) ([]server.SourceRelease, error) {
	base, err := url.Parse(s.URL)
	if err != nil {
		return nil, err
	}
	rc, err := s.get(ctx, s.URL)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var idx index
	if err := json.NewDecoder(io.LimitReader(rc, 16<<20)).Decode(&idx); err != nil {
		return nil, fmt.Errorf("httpindex: decode: %w", err)
	}
	var out []server.SourceRelease
	for _, r := range idx.Releases {
		sr := server.SourceRelease{Tag: r.Tag}
		for _, a := range r.Assets {
			ref, err := url.Parse(a.URL)
			if err != nil {
				return nil, fmt.Errorf("httpindex: asset %s: %w", a.Name, err)
			}
			abs := base.ResolveReference(ref).String()
			sr.Assets = append(sr.Assets, server.Asset{
				Name: a.Name, Size: a.Size,
				Open: func(ctx context.Context) (io.ReadCloser, error) { return s.get(ctx, abs) },
			})
		}
		out = append(out, sr)
	}
	return out, nil
}

func (s *Source) get(ctx context.Context, u string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if s.Authorize != nil {
		s.Authorize(req)
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
		return nil, fmt.Errorf("httpindex: GET %s: %s", u, resp.Status)
	}
	return resp.Body, nil
}

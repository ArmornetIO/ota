package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ArmornetIO/ota"
)

// Transport carries offers, artifact bytes and reports. HTTPTransport talks to
// an ota server; a host with its own channel implements this instead.
type Transport interface {
	// Check returns the offer the server chose for this client, or nil if none.
	Check(ctx context.Context, req ota.CheckRequest) (*ota.SignedOffer, error)
	// Fetch opens the artifact at offset, returning where the body starts.
	Fetch(ctx context.Context, a ota.Artifact, offset int64) (io.ReadCloser, int64, error)
	// Report delivers progress reports.
	Report(ctx context.Context, rs []ota.Report) error
}

// HTTPTransport speaks the ota server's /v1 API.
type HTTPTransport struct {
	// BaseURL is the server root, e.g. https://updates.example.com.
	BaseURL string
	// Client defaults to http.DefaultClient. It must not have a total request
	// timeout shorter than the largest download; use context deadlines instead.
	Client *http.Client
	// Authorize, if set, decorates every request (a bearer token, a signed header).
	Authorize func(*http.Request) error
}

type checkResponse struct {
	Offer         []byte `json:"offer"`
	Signature     []byte `json:"signature"`
	SignatureKind string `json:"signature_kind"`
}

func (t *HTTPTransport) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return http.DefaultClient
}

func (t *HTTPTransport) do(req *http.Request) (*http.Response, error) {
	if t.Authorize != nil {
		if err := t.Authorize(req); err != nil {
			return nil, err
		}
	}
	return t.client().Do(req)
}

func (t *HTTPTransport) url(path string) string {
	return strings.TrimRight(t.BaseURL, "/") + path
}

// Check implements Transport.
func (t *HTTPTransport) Check(ctx context.Context, cr ota.CheckRequest) (*ota.SignedOffer, error) {
	body, _ := json.Marshal(cr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url("/v1/check"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.do(req)
	if err != nil {
		return nil, fmt.Errorf("ota: check: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("ota: check: server returned %s", resp.Status)
	}
	var cr2 checkResponse
	// An offer is a few kilobytes; the cap keeps a hostile server from making
	// the client buffer an unbounded body before anything has been verified.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cr2); err != nil {
		return nil, fmt.Errorf("ota: check: decode: %w", err)
	}
	return &ota.SignedOffer{Offer: cr2.Offer, Signature: cr2.Signature, Kind: cr2.SignatureKind}, nil
}

// Fetch implements Transport.
func (t *HTTPTransport) Fetch(ctx context.Context, a ota.Artifact, offset int64) (io.ReadCloser, int64, error) {
	return rangeGet(ctx, t.do, t.url("/v1/artifacts/"+a.SHA256), offset)
}

// Report implements Transport.
func (t *HTTPTransport) Report(ctx context.Context, rs []ota.Report) error {
	body, _ := json.Marshal(rs)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url("/v1/reports"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ota: report: server returned %s", resp.Status)
	}
	return nil
}

// rangeGet requests rawURL from offset. The standard transport asks for gzip
// on its own only when there is no Range header, which is exactly the rule the
// server applies, so compressed transfer needs no handling here.
func rangeGet(ctx context.Context, do func(*http.Request) (*http.Response, error), rawURL string, offset int64) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := do(req)
	if err != nil {
		return nil, 0, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, 0, nil
	case http.StatusPartialContent:
		start, err := contentRangeStart(resp.Header.Get("Content-Range"))
		if err != nil {
			_ = resp.Body.Close()
			return nil, 0, err
		}
		return resp.Body, start, nil
	}
	_ = resp.Body.Close()
	return nil, 0, fmt.Errorf("ota: fetch %s: %s", redact(rawURL), resp.Status)
}

func contentRangeStart(h string) (int64, error) {
	var start, end, total int64
	if _, err := fmt.Sscanf(h, "bytes %d-%d/%d", &start, &end, &total); err != nil {
		if _, err := fmt.Sscanf(h, "bytes %d-%d/*", &start, &end); err != nil {
			return 0, fmt.Errorf("ota: bad Content-Range %q", h)
		}
	}
	return start, nil
}

// redact drops the query string, which is where signed mirror URLs keep their
// credentials, before a URL reaches an error message or a log.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid url>"
	}
	u.RawQuery = ""
	u.User = nil
	return u.String()
}

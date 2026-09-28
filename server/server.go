// Package server is the ota release server: it chooses which publisher-signed
// offer each client sees, streams artifacts, and records what clients report.
// It holds no signing key and cannot create or alter an offer.
package server

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/verify"
)

// Options configures a Server.
type Options struct {
	Store Store
	Blobs BlobStore
	// Verifier checks every offer before it is stored. Required for
	// ingest and admin uploads; a server that only serves pre-verified offers
	// from its Store may leave it nil.
	Verifier verify.Verifier
	// Source is where releases are ingested from; optional.
	Source Source
	// Targeter, if set, decides per client in place of pins and rollouts.
	Targeter Targeter
	// Authorize admits a client request and may return the caller's client
	// id, which then replaces whatever id the request body claims, so a client
	// cannot report or check as another. Nil admits everything, which is only
	// appropriate behind a host that already authenticates.
	Authorize func(*http.Request) (clientID string, err error)
	// AuthorizeAdmin admits admin requests. Nil disables the admin API.
	AuthorizeAdmin func(*http.Request) error
	// HTTPClient fetches artifacts during ingest. Defaults to a client with
	// no overall timeout (artifacts are large); ingest uses context deadlines.
	HTTPClient *http.Client
	Logger     *slog.Logger
	Now        func() time.Time
}

// Server serves the /v1 API.
type Server struct {
	opts Options
	log  *slog.Logger
}

// New returns a Server.
func New(opts Options) (*Server, error) {
	if opts.Store == nil || opts.Blobs == nil {
		return nil, errors.New("server: Store and Blobs are required")
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{}
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{opts: opts, log: log.With("component", "ota-server")}, nil
}

// Handler returns the API. Mount it at the root of a host's router or under a
// prefix with http.StripPrefix.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/check", s.client(s.check))
	mux.Handle("GET /v1/artifacts/{sha256}", s.client(s.artifact))
	mux.Handle("HEAD /v1/artifacts/{sha256}", s.client(s.artifact))
	mux.Handle("POST /v1/reports", s.client(s.reports))
	s.adminRoutes(mux)
	return mux
}

type clientHandler func(w http.ResponseWriter, r *http.Request, clientID string)

func (s *Server) client(h clientHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ""
		if s.opts.Authorize != nil {
			var err error
			if id, err = s.opts.Authorize(r); err != nil {
				httpError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		h(w, r, id)
	})
}

func (s *Server) check(w http.ResponseWriter, r *http.Request, authID string) {
	var req ota.CheckRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.Program == "" {
		httpError(w, http.StatusBadRequest, "invalid check request")
		return
	}
	if authID != "" {
		req.ClientID = authID
	}
	ctx := r.Context()
	_ = s.opts.Store.TouchClient(ctx, ClientRecord{
		ClientID: req.ClientID, Program: req.Program, OS: req.OS, Arch: req.Arch,
		Channel: req.Channel, RunningVersion: req.RunningVersion, LastSeen: s.now(),
	})

	version, channel, err := s.chooseVersion(ctx, req)
	if err != nil {
		s.log.ErrorContext(ctx, "ota: choose version", "err", err)
		httpError(w, http.StatusInternalServerError, "could not choose an offer")
		return
	}
	if version == "" || version == req.RunningVersion {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	o, err := s.offerFor(ctx, req.Program, version, channel, req.LastOfferIssuedAt)
	if err != nil {
		s.log.ErrorContext(ctx, "ota: find offer", "err", err)
		httpError(w, http.StatusInternalServerError, "could not choose an offer")
		return
	}
	if o == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(o.Signed)
}

func (s *Server) artifact(w http.ResponseWriter, r *http.Request, _ string) {
	ServeBlob(w, r, s.opts.Blobs, r.PathValue("sha256"), BlobOptions{Log: s.log})
}

// BlobOptions tunes ServeBlob for a caller that is not the artifact route.
type BlobOptions struct {
	// Name, when set, is offered as the download filename. The artifact route
	// leaves it empty: an agent asked for a hash and stores it under one.
	Name string
	// Header is applied before anything is written, for a caller that wants to
	// say where the bytes came from.
	Header map[string]string
	// Log receives stream interruptions. Optional.
	Log *slog.Logger
}

// ServeBlob streams one content-addressed artifact.
//
// Exported because there is more than one way to ASK for an artifact and only
// one way to SERVE it. An agent names the digest it was offered; an installer
// names a platform and has the digest resolved for it. Those differ in
// addressing and in who may call them — not in how the bytes leave, and a second
// copy of this drifts: the first one had no ETag, so a resumed 57MB download
// started over, and no compression, so every install paid full size.
func ServeBlob(w http.ResponseWriter, r *http.Request, blobs BlobStore, sum string, opts BlobOptions) {
	// Checked before it reaches the store: it becomes a file name there.
	if !ota.ValidSHA256(sum) {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	f, modTime, err := blobs.Open(r.Context(), sum)
	if errors.Is(err, ErrNotFound) {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		if opts.Log != nil {
			opts.Log.ErrorContext(r.Context(), "ota: open artifact", "sha256", sum, "err", err)
		}
		httpError(w, http.StatusInternalServerError, "could not open artifact")
		return
	}
	defer f.Close()
	for k, v := range opts.Header {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if opts.Name != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+opts.Name+`"`)
	}
	// The content hash is the strongest possible ETag, and If-Range needs a
	// strong one to allow a resumed download.
	w.Header().Set("ETag", `"`+sum+`"`)

	// Range over a compressed stream has no stable meaning, so compression is
	// offered only on whole-file requests; resumes get raw bytes.
	if r.Method == http.MethodGet && r.Header.Get("Range") == "" && acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(w)
		if _, err := io.Copy(gz, f); err != nil && opts.Log != nil {
			opts.Log.WarnContext(r.Context(), "ota: artifact stream interrupted", "sha256", sum, "err", err)
		}
		_ = gz.Close()
		return
	}
	http.ServeContent(w, r, opts.Name, modTime, f)
}

func (s *Server) reports(w http.ResponseWriter, r *http.Request, authID string) {
	var rs []ota.Report
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&rs); err != nil || len(rs) > 64 {
		httpError(w, http.StatusBadRequest, "invalid reports")
		return
	}
	for i := range rs {
		if authID != "" {
			rs[i].ClientID = authID
		}
		// Only the stages clients report are accepted: auto-halt counts them,
		// and a store may enforce the same closed set.
		if rs[i].Program == "" || !rs[i].Stage.Reported() || len(rs[i].Error) > 4096 || len(rs[i].Version) > 128 {
			httpError(w, http.StatusBadRequest, "invalid report")
			return
		}
	}
	ctx := r.Context()
	if err := s.opts.Store.AddReports(ctx, rs); err != nil {
		s.log.ErrorContext(ctx, "ota: store reports", "err", err)
		httpError(w, http.StatusInternalServerError, "could not store reports")
		return
	}
	seen := map[[2]string]bool{}
	for _, rep := range rs {
		k := [2]string{rep.Program, rep.Version}
		if !seen[k] {
			seen[k] = true
			if err := s.checkHalt(ctx, rep.Program, rep.Version); err != nil {
				s.log.ErrorContext(ctx, "ota: check halt", "err", err)
			}
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.TrimSpace(strings.SplitN(part, ";", 2)[0]) == "gzip" {
			return true
		}
	}
	return false
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

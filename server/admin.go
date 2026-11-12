package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ArmornetIO/ota"
)

// adminRoutes are mounted only when the host supplies AuthorizeAdmin, so a
// server cannot be deployed with an open admin API by omission.
func (s *Server) adminRoutes(mux *http.ServeMux) {
	if s.opts.AuthorizeAdmin == nil {
		return
	}
	a := func(h http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := s.opts.AuthorizeAdmin(r); err != nil {
				httpError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			h(w, r)
		})
	}
	mux.Handle("GET /v1/admin/releases", a(s.adminReleases))
	mux.Handle("POST /v1/admin/ingest", a(s.adminIngest))
	mux.Handle("POST /v1/admin/offers", a(s.adminAddOffer))
	mux.Handle("GET /v1/admin/rollouts", a(s.adminListRollouts))
	mux.Handle("PUT /v1/admin/rollouts/{program}/{channel}", a(s.adminPutRollout))
	mux.Handle("POST /v1/admin/rollouts/{program}/{channel}/resume", a(s.adminResumeRollout))
	mux.Handle("PUT /v1/admin/pins/{program}/{client_id}", a(s.adminPutPin))
	mux.Handle("DELETE /v1/admin/pins/{program}/{client_id}", a(s.adminDeletePin))
	mux.Handle("GET /v1/admin/clients", a(s.adminClients))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ReleaseView is one release as GET /v1/admin/releases lists it. Offer bytes
// are for clients; the admin view says what was decided and why. Untagged, so
// its keys are the field names.
type ReleaseView struct {
	Program, Version, Tag, Source, Status, Reason string
	Offers                                        int
}

func (s *Server) adminReleases(w http.ResponseWriter, r *http.Request) {
	rels, err := s.opts.Store.Releases(r.Context(), r.URL.Query().Get("program"))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "could not list releases")
		return
	}
	out := make([]ReleaseView, 0, len(rels))
	for _, rel := range rels {
		out = append(out, ReleaseView{rel.Program, rel.Version, rel.Tag, rel.Source, rel.Status, rel.Reason, len(rel.Offers)})
	}
	writeJSON(w, out)
}

func (s *Server) adminIngest(w http.ResponseWriter, r *http.Request) {
	res, err := s.Ingest(r.Context())
	if err != nil {
		httpError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, res)
}

func (s *Server) adminAddOffer(w http.ResponseWriter, r *http.Request) {
	var so ota.SignedOffer
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&so); err != nil {
		httpError(w, http.StatusBadRequest, "invalid signed offer")
		return
	}
	rel, err := s.AddOffer(r.Context(), so, "admin", "")
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]string{"program": rel.Program, "version": rel.Version, "status": rel.Status})
}

func (s *Server) adminListRollouts(w http.ResponseWriter, r *http.Request) {
	rs, err := s.opts.Store.Rollouts(r.Context(), r.URL.Query().Get("program"))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "could not list rollouts")
		return
	}
	writeJSON(w, rs)
}

type rolloutRequest struct {
	Version         string   `json:"version"`
	Percent         int      `json:"percent"`
	HaltThreshold   *float64 `json:"halt_threshold"`
	HaltMinAttempts *int     `json:"halt_min_attempts"`
}

func (s *Server) adminPutRollout(w http.ResponseWriter, r *http.Request) {
	program, channel := r.PathValue("program"), r.PathValue("channel")
	var req rolloutRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid rollout")
		return
	}
	ro := Rollout{
		Program: program, Channel: channel, Version: strings.TrimPrefix(req.Version, "v"), Percent: req.Percent,
		HaltThreshold: DefaultHaltThreshold, HaltMinAttempts: DefaultHaltMinAttempts,
		State: RolloutActive, UpdatedAt: s.now(),
	}
	if req.HaltThreshold != nil {
		ro.HaltThreshold = *req.HaltThreshold
	}
	if req.HaltMinAttempts != nil {
		ro.HaltMinAttempts = *req.HaltMinAttempts
	}
	if err := s.SetRollout(r.Context(), ro); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, ro)
}

func (s *Server) adminResumeRollout(w http.ResponseWriter, r *http.Request) {
	program, channel := r.PathValue("program"), r.PathValue("channel")
	rs, err := s.opts.Store.Rollouts(r.Context(), program)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "could not load rollouts")
		return
	}
	for _, ro := range rs {
		if ro.Channel == channel {
			ro.State, ro.Reason, ro.UpdatedAt = RolloutActive, "resumed by admin", s.now()
			if err := s.opts.Store.PutRollout(r.Context(), ro); err != nil {
				httpError(w, http.StatusInternalServerError, "could not resume")
				return
			}
			writeJSON(w, ro)
			return
		}
	}
	httpError(w, http.StatusNotFound, "no rollout for that channel")
}

func (s *Server) adminPutPin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid pin")
		return
	}
	p := Pin{Program: r.PathValue("program"), ClientID: r.PathValue("client_id"), Version: strings.TrimPrefix(req.Version, "v")}
	if err := s.SetPin(r.Context(), p); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, p)
}

func (s *Server) adminDeletePin(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Store.DeletePin(r.Context(), r.PathValue("program"), r.PathValue("client_id")); err != nil {
		httpError(w, http.StatusInternalServerError, "could not delete pin")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminClients(w http.ResponseWriter, r *http.Request) {
	cs, err := s.opts.Store.Clients(r.Context(), r.URL.Query().Get("program"))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "could not list clients")
		return
	}
	if v := r.URL.Query().Get("version"); v != "" {
		kept := cs[:0]
		for _, c := range cs {
			if c.RunningVersion == v {
				kept = append(kept, c)
			}
		}
		cs = kept
	}
	writeJSON(w, cs)
}

// SetRollout validates and stores a rollout. The version must have a signed
// offer admitting the channel, so a rollout can never point at nothing.
func (s *Server) SetRollout(ctx context.Context, ro Rollout) error {
	if !ota.ValidChannel(ro.Channel) || ro.Percent < 0 || ro.Percent > 100 ||
		ro.HaltThreshold < 0 || ro.HaltThreshold > 1 || ro.HaltMinAttempts < 0 {
		return errInvalid
	}
	if _, err := ota.ParseVersion(ro.Version); err != nil {
		return errInvalid
	}
	if err := s.requireSignedVersion(ctx, ro.Program, ro.Version, ro.Channel); err != nil {
		return err
	}
	if ro.State == "" {
		ro.State = RolloutActive
	}
	return s.opts.Store.PutRollout(ctx, ro)
}

// SetPin validates and stores a pin against a signed version.
func (s *Server) SetPin(ctx context.Context, p Pin) error {
	if p.ClientID == "" || len(p.ClientID) > 256 {
		return errInvalid
	}
	if _, err := ota.ParseVersion(p.Version); err != nil {
		return errInvalid
	}
	if err := s.requireSignedVersion(ctx, p.Program, p.Version, ""); err != nil {
		return err
	}
	return s.opts.Store.PutPin(ctx, p)
}

var errInvalid = errors.New("server: invalid request")

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoRelease):
		httpError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errInvalid):
		httpError(w, http.StatusBadRequest, err.Error())
	default:
		httpError(w, http.StatusInternalServerError, "could not save")
	}
}

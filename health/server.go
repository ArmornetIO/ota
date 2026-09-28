package health

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// This file is the producing side of the same vocabulary the Gate consumes: a
// program registers component checkers, and serves the cached rollup that
// ProbeURL (or StatusFunc over Cached) later reads. Keeping both halves in one
// package is what stops the reporter and the judge drifting apart.

// statusRank drives worst-wins rollup. Higher rank = worse.
var statusRank = map[string]int{
	StatusOK:               0,
	StatusRecoverableError: 1,
	StatusPermanentError:   2,
	StatusStarting:         3,
	StatusStopping:         4,
	StatusStopped:          5,
	StatusFatalError:       6,
}

// Worse returns the more severe of two status strings.
func Worse(a, b string) string {
	if statusRank[b] > statusRank[a] {
		return b
	}
	return a
}

// HTTPStatus maps a component status to an HTTP status code, following the
// OTel health check extension v2 convention.
func HTTPStatus(s string) int {
	switch s {
	case StatusOK, StatusRecoverableError, StatusPermanentError:
		return http.StatusOK
	case StatusFatalError:
		return http.StatusInternalServerError
	default: // Starting, Stopping, Stopped
		return http.StatusServiceUnavailable
	}
}

// ComponentHealth is the per-component health evaluation included in a Report.
type ComponentHealth struct {
	Status    string         `json:"status"`
	Reason    string         `json:"reason,omitempty"`
	LatencyMS *int64         `json:"latency_ms,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

// Report is the top-level structured body returned by GET /status.
type Report struct {
	Status string                     `json:"status"`
	Checks map[string]ComponentHealth `json:"checks"`
}

// Config controls the health server's polling behaviour.
type Config struct {
	// PollInterval defaults to 10s if zero.
	PollInterval time.Duration
}

// Server runs registered checker functions on a schedule and caches the result.
// Serve /status and /healthz from Cached() — never block a request on live checks.
type Server struct {
	cfg    Config
	mu     sync.RWMutex
	checks map[string]func() ComponentHealth
	cached Report
}

// NewServer returns a Server ready to accept Register calls.
func NewServer(cfg Config) *Server {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 10 * time.Second
	}
	return &Server{
		cfg:    cfg,
		checks: make(map[string]func() ComponentHealth),
		cached: Report{
			Status: StatusStarting,
			Checks: map[string]ComponentHealth{},
		},
	}
}

// Register adds a named health checker. fn must be non-blocking; it is called
// concurrently with other checkers during Poll. Calling Register after Start
// is safe — the next Poll will include the new checker.
func (s *Server) Register(name string, fn func() ComponentHealth) {
	s.mu.Lock()
	s.checks[name] = fn
	s.mu.Unlock()
}

// Poll runs all registered checkers concurrently and atomically replaces the
// cached Report. Callers may invoke Poll directly for on-demand refresh.
func (s *Server) Poll(ctx context.Context) {
	s.mu.RLock()
	names := make([]string, 0, len(s.checks))
	fns := make([]func() ComponentHealth, 0, len(s.checks))
	for name, fn := range s.checks {
		names = append(names, name)
		fns = append(fns, fn)
	}
	s.mu.RUnlock()

	type entry struct {
		name string
		ch   ComponentHealth
	}

	out := make(chan entry, len(fns))
	var wg sync.WaitGroup
	wg.Add(len(fns))
	for i := range fns {
		go func() {
			defer wg.Done()
			out <- entry{names[i], fns[i]()}
		}()
	}
	go func() { wg.Wait(); close(out) }()

	checks := make(map[string]ComponentHealth, len(fns))
	overall := StatusOK
	for e := range out {
		checks[e.name] = e.ch
		overall = Worse(overall, e.ch.Status)
	}

	s.mu.Lock()
	s.cached = Report{Status: overall, Checks: checks}
	s.mu.Unlock()
}

// Cached returns the last polled Report without running checks. Initial value
// has status Starting until the first Poll completes.
func (s *Server) Cached() Report {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cached
}

// Start warms the cache immediately then polls on PollInterval until ctx is
// cancelled. It marks the cached report Stopping on exit.
func (s *Server) Start(ctx context.Context) error {
	s.Poll(ctx)
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.Poll(ctx)
		case <-ctx.Done():
			s.mu.Lock()
			s.cached.Status = StatusStopping
			s.mu.Unlock()
			return nil
		}
	}
}

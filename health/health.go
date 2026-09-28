// Package health decides whether a freshly installed version is healthy enough
// to keep. The status vocabulary is the OpenTelemetry Collector's component
// status, so a program that already reports health that way plugs in directly.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// The OpenTelemetry Collector component status values. Defined here rather
// than imported: the updater core takes no dependency on any health library.
const (
	StatusStarting         = "Starting"
	StatusOK               = "OK"
	StatusRecoverableError = "RecoverableError"
	StatusPermanentError   = "PermanentError"
	StatusFatalError       = "FatalError"
	StatusStopping         = "Stopping"
	StatusStopped          = "Stopped"
)

// Source reports the new version's current status.
type Source interface {
	Status(ctx context.Context) (string, error)
}

// SourceFunc adapts a function to Source.
type SourceFunc func(ctx context.Context) (string, error)

// Status implements Source.
func (f SourceFunc) Status(ctx context.Context) (string, error) { return f(ctx) }

// StatusFunc wraps an in-process status getter, such as a health server's
// cached rollup.
func StatusFunc(f func() string) Source {
	return SourceFunc(func(context.Context) (string, error) { return f(), nil })
}

// ErrCommitOnly marks a Source that never reports on its own; only an explicit
// Commit from the program counts.
var ErrCommitOnly = errors.New("health: commit-only source")

// CommitOnly is for programs with no health reporting: the gate waits for the
// program to call Commit, and rolls back if the deadline passes first.
func CommitOnly() Source {
	return SourceFunc(func(context.Context) (string, error) { return "", ErrCommitOnly })
}

// ProbeURL polls a local health endpoint. A JSON body with a "status" field in
// the OpenTelemetry vocabulary wins; otherwise the HTTP code decides: 2xx is
// OK, 503 is Starting (the conventional "not ready yet"), any other 5xx is
// FatalError, and anything else is RecoverableError.
func ProbeURL(url string) Source {
	client := &http.Client{Timeout: 5 * time.Second}
	return SourceFunc(func(ctx context.Context) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			// Not listening yet is the normal state just after a restart.
			return StatusStarting, nil
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var doc struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(body, &doc) == nil && Known(doc.Status) {
			return doc.Status, nil
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return StatusOK, nil
		case resp.StatusCode == http.StatusServiceUnavailable:
			return StatusStarting, nil
		case resp.StatusCode >= 500:
			return StatusFatalError, nil
		}
		return StatusRecoverableError, nil
	})
}

// Known reports whether s is one of the seven status values.
func Known(s string) bool {
	switch s {
	case StatusStarting, StatusOK, StatusRecoverableError, StatusPermanentError,
		StatusFatalError, StatusStopping, StatusStopped:
		return true
	}
	return false
}

package ota

import "time"

// Stage is a point in an update attempt, reported to the server and emitted as
// an event. The set is closed: rollout auto-halt counts specific stages, so an
// ad-hoc string would silently fall out of that arithmetic.
type Stage string

const (
	StageOfferSeen   Stage = "offer_seen"
	StageDeferred    Stage = "deferred"
	StageRefused     Stage = "refused"
	StageDownloading Stage = "downloading"
	StageDownloaded  Stage = "downloaded"
	StageVerified    Stage = "verified"
	StageSwapped     Stage = "swapped"
	StageInstalled   Stage = "installed"
	StageHealthy     Stage = "healthy"
	StageCommitted   Stage = "committed"
	StageRolledBack  Stage = "rolled_back"
	StageFailed      Stage = "failed"
	StageStaged      Stage = "staged"
)

// Reported is whether a stage is sent to the server. The rest are local
// progress: useful to a host watching events, noise in a server's report.
func (s Stage) Reported() bool {
	switch s {
	case StageDownloading, StageVerified, StageInstalled, StageCommitted,
		StageFailed, StageRolledBack, StageRefused, StageDeferred:
		return true
	}
	return false
}

// Event is emitted to Options.OnEvent at every stage transition.
type Event struct {
	Stage   Stage
	Program string
	Version string
	// Bytes and Total are set on download progress events.
	Bytes, Total int64
	Err          error
	At           time.Time
}

// Report is one reported stage of one attempt, as sent to the server.
type Report struct {
	ClientID      string `json:"client_id"`
	Program       string `json:"program"`
	Version       string `json:"version"`
	OfferIssuedAt int64  `json:"offer_issued_at"`
	Stage         Stage  `json:"stage"`
	Error         string `json:"error,omitempty"`
	At            int64  `json:"at"`
}

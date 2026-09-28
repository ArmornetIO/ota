package server

import (
	"context"
	"errors"
	"time"

	"github.com/ArmornetIO/ota"
)

// Release status values.
const (
	ReleaseIngested = "ingested"
	ReleaseRejected = "rejected"
)

// Rollout states.
const (
	RolloutActive   = "active"
	RolloutHalted   = "halted"
	RolloutComplete = "complete"
)

// Release is a published version as the server knows it.
type Release struct {
	Program    string        `json:"program"`
	Version    string        `json:"version"`
	Tag        string        `json:"tag,omitempty"`
	Source     string        `json:"source,omitempty"`
	Status     string        `json:"status"`
	Reason     string        `json:"reason,omitempty"`
	IngestedAt time.Time     `json:"ingested_at"`
	Offers     []StoredOffer `json:"offers"`
}

// StoredOffer is one publisher-signed offer, kept byte for byte.
type StoredOffer struct {
	Signed   ota.SignedOffer `json:"signed"`
	IssuedAt int64           `json:"issued_at"`
	// ExpiresAt is copied from the offer so an expired one is never served;
	// clients would refuse it anyway, on every check.
	ExpiresAt int64    `json:"expires_at,omitempty"`
	Channels  []string `json:"channels,omitempty"`
	// Digest identifies the offer bytes, so re-ingesting is a no-op.
	Digest string `json:"digest"`
}

// Rollout offers a version to a share of a channel.
type Rollout struct {
	Program         string    `json:"program"`
	Channel         string    `json:"channel"`
	Version         string    `json:"version"`
	Percent         int       `json:"percent"`
	HaltThreshold   float64   `json:"halt_threshold"`
	HaltMinAttempts int       `json:"halt_min_attempts"`
	State           string    `json:"state"`
	Reason          string    `json:"reason,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Pin fixes one client to one version.
type Pin struct {
	Program  string `json:"program"`
	ClientID string `json:"client_id"`
	Version  string `json:"version"`
}

// ClientRecord is the latest the server knows about one installation.
type ClientRecord struct {
	ClientID       string      `json:"client_id"`
	Program        string      `json:"program"`
	OS             string      `json:"os"`
	Arch           string      `json:"arch"`
	Channel        string      `json:"channel,omitempty"`
	RunningVersion string      `json:"running_version"`
	LastSeen       time.Time   `json:"last_seen"`
	LastReport     *ota.Report `json:"last_report,omitempty"`
}

// Outcomes counts distinct clients per outcome for one version, which is
// what auto-halt decides on.
type Outcomes struct {
	Attempted int
	Failed    int
}

// ErrNoRelease reports a version with no ingested signed offer.
var ErrNoRelease = errors.New("server: no ingested signed offer for that version")

// Store persists what the server decides from. The server holds no key and
// stores offers only as the bytes the publisher signed. Hosts implement it
// over their own database; MemStore and FileStore are included.
//
// Reports and client records link an installation to what it runs; a host
// whose installations belong to organisations should encrypt them at rest.
type Store interface {
	PutRelease(ctx context.Context, r Release) error
	Releases(ctx context.Context, program string) ([]Release, error)

	PutRollout(ctx context.Context, r Rollout) error
	Rollouts(ctx context.Context, program string) ([]Rollout, error)

	PutPin(ctx context.Context, p Pin) error
	DeletePin(ctx context.Context, program, clientID string) error
	GetPin(ctx context.Context, program, clientID string) (*Pin, error)

	TouchClient(ctx context.Context, c ClientRecord) error
	Clients(ctx context.Context, program string) ([]ClientRecord, error)

	// AddReports stores reports, ignoring exact repeats (same client,
	// version, offer and stage), and updates each client's record.
	AddReports(ctx context.Context, rs []ota.Report) error
	Outcomes(ctx context.Context, program, version string) (Outcomes, error)
}

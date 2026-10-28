package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"slices"
	"time"

	"github.com/ArmornetIO/ota"
)

// Defaults for a rollout that does not set its own halt rule.
const (
	DefaultHaltThreshold   = 0.05
	DefaultHaltMinAttempts = 50
)

// Target is a host's decision for one client. Returned with ok=false from a
// Targeter, the built-in rules decide instead.
type Target struct {
	// Deny offers nothing.
	Deny bool
	// Version, if set, is offered regardless of rollouts (a host-side pin).
	Version string
	// Channel, if set, replaces the client's claimed channel for rollouts.
	Channel string
}

// Targeter lets a host decide who gets what (for example per organisation),
// in place of built-in pins and rollouts. It can only choose among signed
// offers; it cannot create one.
type Targeter interface {
	Target(ctx context.Context, req ota.CheckRequest) (Target, bool, error)
}

// Bucket places a client in 0–99 for a program and channel. Stable, so a
// client stays in or out of a rollout across checks, and independent per
// channel, so the same clients are not always first.
func Bucket(program, channel, clientID string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(program + "/" + channel + "/" + clientID))
	return int(h.Sum64() % 100)
}

// chooseVersion runs targeting, pins, then rollouts. An empty version means
// nothing is offered.
func (s *Server) chooseVersion(ctx context.Context, req ota.CheckRequest) (string, string, error) {
	channel := req.Channel
	if s.opts.Targeter != nil {
		t, ok, err := s.opts.Targeter.Target(ctx, req)
		if err != nil {
			return "", "", err
		}
		if ok {
			if t.Deny {
				return "", channel, nil
			}
			if t.Version != "" {
				return t.Version, channel, nil
			}
			if t.Channel != "" {
				channel = t.Channel
			}
		}
	}
	pin, err := s.opts.Store.GetPin(ctx, req.Program, req.ClientID)
	if err != nil {
		return "", "", err
	}
	if pin != nil {
		return pin.Version, channel, nil
	}
	rollouts, err := s.opts.Store.Rollouts(ctx, req.Program)
	if err != nil {
		return "", "", err
	}
	for _, r := range rollouts {
		if r.Channel != channel || r.State == RolloutHalted {
			continue
		}
		if Bucket(req.Program, channel, req.ClientID) < r.Percent {
			return r.Version, channel, nil
		}
	}
	return "", channel, nil
}

// offerFor returns the newest ingested offer for version that admits channel
// and is newer than what the client last acted on.
func (s *Server) offerFor(ctx context.Context, program, version, channel string, after int64) (*StoredOffer, error) {
	return s.findOffer(ctx, program, version, channel, after, s.now().UnixMilli())
}

// findOffer is offerFor with an explicit clock; now 0 ignores expiry.
func (s *Server) findOffer(ctx context.Context, program, version, channel string, after, now int64) (*StoredOffer, error) {
	rels, err := s.opts.Store.Releases(ctx, program)
	if err != nil {
		return nil, err
	}
	var best *StoredOffer
	for _, r := range rels {
		if r.Status != ReleaseIngested || r.Version != version {
			continue
		}
		for i := range r.Offers {
			o := r.Offers[i]
			if o.IssuedAt <= after || (len(o.Channels) > 0 && !slices.Contains(o.Channels, channel)) {
				continue
			}
			if now != 0 && o.ExpiresAt != 0 && o.ExpiresAt <= now {
				continue
			}
			if best == nil || o.IssuedAt > best.IssuedAt {
				best = &o
			}
		}
	}
	return best, nil
}

// requireSignedVersion: pins and rollouts can only name a version
// the publisher signed an offer for. Expiry does not count here: a rollout may
// point at a version whose re-signed offer has not landed yet, and nothing is
// served until it has.
func (s *Server) requireSignedVersion(ctx context.Context, program, version, channel string) error {
	o, err := s.findOffer(ctx, program, version, channel, 0, 0)
	if err != nil {
		return err
	}
	if o == nil {
		return fmt.Errorf("%w: %s %s", ErrNoRelease, program, version)
	}
	return nil
}

// checkHalt halts active rollouts of a version whose failure rate crossed the
// rollout's threshold over at least its minimum number of attempts.
func (s *Server) checkHalt(ctx context.Context, program, version string) error {
	rollouts, err := s.opts.Store.Rollouts(ctx, program)
	if err != nil {
		return err
	}
	for _, r := range rollouts {
		if r.Version != version || r.State != RolloutActive {
			continue
		}
		out, err := s.opts.Store.Outcomes(ctx, program, version)
		if err != nil {
			return err
		}
		if out.Attempted < r.HaltMinAttempts || out.Attempted == 0 {
			continue
		}
		if rate := float64(out.Failed) / float64(out.Attempted); rate > r.HaltThreshold {
			r.State = RolloutHalted
			r.Reason = fmt.Sprintf("halted automatically: %d of %d attempts failed (%.1f%% > %.1f%%)",
				out.Failed, out.Attempted, rate*100, r.HaltThreshold*100)
			r.UpdatedAt = s.now()
			s.log.WarnContext(ctx, "ota: rollout halted", "program", program, "channel", r.Channel, "version", version, "reason", r.Reason)
			if err := s.opts.Store.PutRollout(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func storedOffer(so ota.SignedOffer, o ota.Offer) StoredOffer {
	sum := sha256.Sum256(so.Offer)
	return StoredOffer{Signed: so, IssuedAt: o.IssuedAt, ExpiresAt: o.ExpiresAt, Channels: o.Channels, Digest: hex.EncodeToString(sum[:])}
}

func (s *Server) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

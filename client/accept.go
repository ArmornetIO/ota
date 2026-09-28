package client

import (
	"fmt"
	"time"

	"github.com/ArmornetIO/ota"
)

// acceptance is everything an offer is judged against, after its signature
// has verified. Kept separate from Client so the rules are testable as a table.
type acceptance struct {
	program   string
	channel   string
	goos      string
	goarch    string
	running   ota.Version
	lastIssue int64
	now       time.Time
	tolerance time.Duration
}

// verdict is the result of the acceptance rules.
type verdict struct {
	artifact ota.Artifact
	upToDate bool
}

// accept applies rules 2–8 of the offer contract in order; rule 1, the
// signature, has already passed. A returned error wraps ota.ErrRefused.
func (a acceptance) accept(o ota.Offer) (verdict, error) {
	refuse := func(format string, args ...any) (verdict, error) {
		return verdict{}, fmt.Errorf("%w: %s", ota.ErrRefused, fmt.Sprintf(format, args...))
	}
	nowMs := a.now.UnixMilli()
	tol := a.tolerance.Milliseconds()

	if o.Program != a.program {
		return refuse("offer is for %q, this is %q", o.Program, a.program)
	}
	if o.IssuedAt <= a.lastIssue {
		return refuse("offer issued at %d is not newer than the last accepted %d (replay)", o.IssuedAt, a.lastIssue)
	}
	if o.IssuedAt > nowMs+tol {
		return refuse("offer is issued %s in the future", time.Duration(o.IssuedAt-nowMs)*time.Millisecond)
	}
	if o.ExpiresAt != 0 && o.ExpiresAt <= nowMs-tol {
		return refuse("offer expired at %s", time.UnixMilli(o.ExpiresAt).UTC().Format(time.RFC3339))
	}
	if !o.AdmitsChannel(a.channel) {
		return refuse("offer is not for channel %q", a.channel)
	}
	switch c := o.SemVer().Compare(a.running); {
	case c == 0:
		return verdict{upToDate: true}, nil
	case c < 0 && !o.Downgrade:
		return refuse("offer version %s is older than running %s and is not marked downgrade", o.Version, a.running)
	}
	art, ok := o.ArtifactFor(a.goos, a.goarch)
	if !ok {
		return refuse("offer has no artifact for %s/%s", a.goos, a.goarch)
	}
	return verdict{artifact: art}, nil
}

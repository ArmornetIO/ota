package ota

import "time"

// Action is the host's answer to an offer.
type Action int

const (
	Apply Action = iota
	Defer
	Refuse
)

// Decision is what a BeforeApply hook returns. ota never prompts anyone; a host
// that wants a person to approve asks them inside the hook and answers here.
type Decision struct {
	Action Action
	// Until is when a deferred offer may be reconsidered.
	Until  time.Time
	Reason string
}

// ApplyNow is the Decision that proceeds immediately.
func ApplyNow() Decision { return Decision{Action: Apply} }

// DeferUntil postpones the offer; it is reconsidered at or after t.
func DeferUntil(t time.Time, reason string) Decision {
	return Decision{Action: Defer, Until: t, Reason: reason}
}

// RefuseOffer declines this offer; it is reported and not retried.
func RefuseOffer(reason string) Decision { return Decision{Action: Refuse, Reason: reason} }

// Package restart decides how a swapped-in version starts running. Each
// strategy contributes named lifecycle steps, so an adopter can insert, remove
// or reorder steps without replacing the strategy.
package restart

import "github.com/ArmornetIO/ota/lifecycle"

// Step names in the chains strategies build. Adopters address steps by these.
const (
	StepSwap    = "swap"
	StepRestart = "restart"
)

// Plan is what a strategy needs to act. The client prepares Install and
// Restore, so a strategy never touches the on-disk layout directly.
type Plan struct {
	// Binary is the installed program's path.
	Binary string
	// StateDir is where ota keeps this program's state.
	StateDir string
	// Version is the version being installed.
	Version string
	// Install swaps the verified new binary into place.
	Install func() error
	// Restore puts the previous binary back.
	Restore func() error
	// BeforeRestart runs just before the process is replaced; the client
	// releases its lock here so the new process can take it.
	BeforeRestart func()
	// DeferGate marks the installed version as not started yet, so its commit
	// deadline begins at its first start rather than now. For strategies that
	// install without restarting.
	DeferGate func() error
	// Staged is the verified download, for strategies that install it
	// somewhere other than over Binary (side-by-side).
	Staged string
	// Commit records the attempt as committed, for strategies that run the
	// health gate themselves instead of in the new process's Boot.
	Commit func() error
}

// Restarter builds the steps that take a verified download to a running new
// version. When a step fails, the chain stops the steps already started in
// reverse, which is how a failed restart puts the old binary back.
type Restarter interface {
	Chain(p Plan) []lifecycle.Hook
	// Relaunch starts binary in place of the current process after a rollback.
	Relaunch(binary string) error
}

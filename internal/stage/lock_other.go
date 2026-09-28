//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package stage

import (
	"errors"
	"os"
)

// Updating without a lock would let two updaters interleave a swap, so an
// unsupported platform refuses rather than proceeding unlocked.
func lockFile(*os.File) error {
	return errors.New("stage: file locking is not supported on this platform")
}
func unlockFile(*os.File) error { return nil }

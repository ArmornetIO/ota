//go:build !windows

package restart

import (
	"fmt"
	"syscall"
)

// execSelf replaces the process image. It returns only on failure, and the
// chain then restores the previous binary.
func execSelf(binary string, args, env []string) error {
	// binary is this program's own install path and args/env are its own; no
	// external input reaches them, and re-running itself is the point.
	// #nosec G204 G702 -- the program's own install path and its own argv
	if err := syscall.Exec(binary, args, env); err != nil { //nolint:gosec
		return fmt.Errorf("restart: exec %s: %w", binary, err)
	}
	return nil
}

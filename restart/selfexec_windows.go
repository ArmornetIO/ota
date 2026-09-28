//go:build windows

package restart

import (
	"fmt"
	"os"
	"os/exec"
)

// execSelf starts the new binary with the same arguments and standard streams
// and exits. Windows has no execve, so this is the closest equivalent; the
// process id changes, which is why services should use a service strategy.
func execSelf(binary string, args, env []string) error {
	// binary is this program's own install path and args are its own; no
	// external input reaches them, and re-running itself is the point.
	// #nosec G204 -- the program's own install path and its own argv
	cmd := exec.Command(binary, args[1:]...) //nolint:gosec
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart: start %s: %w", binary, err)
	}
	os.Exit(0)
	return nil
}

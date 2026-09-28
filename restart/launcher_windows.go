//go:build windows

package restart

import (
	"errors"
	"os"
	"syscall"
)

// Windows has no SIGTERM to deliver to another process; the child is ended.
func interrupt(p *os.Process) error { return p.Kill() }

func signalOf(*os.ProcessState) syscall.Signal { return 0 }

// reexecSelf is unsupported: without execve the launcher cannot become the new
// version while keeping its child. It keeps running the old launcher code,
// which only supervises, until its next restart.
func reexecSelf(string, int, string) error {
	return errors.New("restart: launcher re-exec is not supported on Windows")
}

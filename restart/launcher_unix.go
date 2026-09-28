//go:build !windows

package restart

import (
	"os"
	"strconv"
	"syscall"
)

func interrupt(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

func signalOf(st *os.ProcessState) syscall.Signal {
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ws.Signal()
	}
	return 0
}

// reexecSelf replaces the launcher with the binary now on disk. execve keeps
// the pid, so the child is still this process's child afterwards and the new
// launcher can wait on it: the program keeps running through the switch.
func reexecSelf(binary string, childPid int, fromVersion string) error {
	env := append(os.Environ(),
		envAdopt+"="+strconv.Itoa(childPid),
		envReexecFrom+"="+fromVersion,
	)
	// #nosec G204 G702 -- the program's own install path and its own argv
	return syscall.Exec(binary, os.Args, env) //nolint:gosec
}

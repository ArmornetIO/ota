//go:build linux || darwin || freebsd

package stage

import "syscall"

// Free returns the bytes available to an unprivileged writer in dir.
func Free(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}

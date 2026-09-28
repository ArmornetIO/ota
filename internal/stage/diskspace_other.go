//go:build !(linux || darwin || freebsd || windows)

package stage

// Free reports that free space is unknown; the download then relies on ENOSPC.
func Free(string) (uint64, bool) { return 0, false }

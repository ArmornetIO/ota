//go:build windows

package stage

// SyncDir is a no-op: Windows cannot open a directory for FlushFileBuffers,
// and NTFS journals the rename metadata itself.
func SyncDir(string) error { return nil }

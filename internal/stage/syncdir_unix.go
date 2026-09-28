//go:build !windows

package stage

import "os"

// SyncDir makes a rename in dir durable. Without it a power cut can undo a
// rename that already returned, leaving a state file pointing at nothing.
func SyncDir(dir string) error {
	d, err := os.Open(dir) // #nosec G304 -- opens the stage dir itself, only to fsync it
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

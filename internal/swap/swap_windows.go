//go:build windows

package swap

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ArmornetIO/ota/internal/testkit"
)

// install moves the running executable aside, then moves the new one into
// place. Windows refuses to overwrite a running .exe but allows renaming it,
// so this is two renames with a short window between them that Repair covers.
func install(l Layout, newPath string) error {
	if err := os.Rename(l.Binary, l.Prev()); err != nil {
		return fmt.Errorf("swap: move running binary aside: %w", err)
	}
	testkit.KillAt("after-link")
	if err := os.Rename(newPath, l.Binary); err != nil {
		_ = os.Rename(l.Prev(), l.Binary)
		return fmt.Errorf("swap: install new binary: %w", err)
	}
	return nil
}

// restore moves the failed binary aside first for the same reason: it may be
// the process performing the restore.
func restore(l Layout) error {
	bad := filepath.Join(l.StateDir, "bad-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.Rename(l.Binary, bad); err != nil {
		return fmt.Errorf("swap: move failed binary aside: %w", err)
	}
	if err := os.Rename(l.Prev(), l.Binary); err != nil {
		_ = os.Rename(bad, l.Binary)
		return fmt.Errorf("swap: restore previous binary: %w", err)
	}
	return nil
}

//go:build !windows

package swap

import (
	"fmt"
	"os"

	"github.com/ArmornetIO/ota/internal/testkit"
)

// install hard-links the current binary to prev, then renames the new one over
// it. rename(2) replaces atomically, so the install path always holds a
// complete binary; there is no window with nothing there.
func install(l Layout, newPath string) error {
	if err := os.Link(l.Binary, l.Prev()); err != nil {
		return fmt.Errorf("swap: keep previous binary: %w", err)
	}
	testkit.KillAt("after-link")
	if err := os.Rename(newPath, l.Binary); err != nil {
		return fmt.Errorf("swap: install new binary: %w", err)
	}
	return nil
}

func restore(l Layout) error {
	if err := os.Rename(l.Prev(), l.Binary); err != nil {
		return fmt.Errorf("swap: restore previous binary: %w", err)
	}
	return nil
}

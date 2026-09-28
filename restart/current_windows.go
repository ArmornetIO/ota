//go:build windows

package restart

import (
	"os"
	"path/filepath"
	"strings"
)

// setCurrent records the live version in <root>\current as a one-line file.
// Creating a symlink needs a privilege Windows users usually lack, and every
// service already points at its own versioned path, so a pointer file is all
// "current" has to be. Replaced by rename, so the switch is atomic.
func setCurrent(root, version string) error {
	tmp := filepath.Join(root, "current.new")
	if err := os.WriteFile(tmp, []byte(version+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(root, "current"))
}

func readCurrent(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "current"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

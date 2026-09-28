//go:build !windows

package restart

import (
	"os"
	"path/filepath"
	"strings"
)

// setCurrent points <root>/current at versions/<version>. A symlink created
// beside it and renamed over it switches in one atomic step, so a reader sees
// either the old version or the new, never a missing link.
func setCurrent(root, version string) error {
	tmp := filepath.Join(root, "current.new")
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("versions", version), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(root, "current"))
}

func readCurrent(root string) (string, error) {
	target, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(filepath.ToSlash(target), "versions/"), nil
}

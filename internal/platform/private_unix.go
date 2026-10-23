//go:build !windows

package platform

import "os"

// ProtectPrivate restricts credentials and their containing directories to the
// current user. Directories retain the traversal permission required to use them.
func ProtectPrivate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if info.IsDir() {
		mode = 0700
	}
	return os.Chmod(path, mode)
}

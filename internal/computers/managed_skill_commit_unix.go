//go:build !windows

package computers

import (
	"errors"
	"os"
)

func moveManagedSkillPath(from, to string, _ bool) error { return os.Rename(from, to) }

func syncManagedSkillDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

package computers

import (
	"strings"

	"golang.org/x/sys/windows"
)

// FlushFileBuffers does not support a directory handle. The staged files are
// synced explicitly and their same-volume rename uses write-through.
func syncManagedSkillDir(string) error { return nil }

func moveManagedSkillPath(from, to string, replace bool) error {
	absolute := func(name string) string {
		if strings.HasPrefix(name, `\\?\`) {
			return name
		}
		if strings.HasPrefix(name, `\\`) {
			return `\\?\UNC\` + name[2:]
		}
		return `\\?\` + name
	}
	old, err := windows.UTF16PtrFromString(absolute(from))
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(absolute(to))
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(old, target, flags)
}

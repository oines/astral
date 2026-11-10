package skills

import (
	"strings"

	"golang.org/x/sys/windows"
)

// FlushFileBuffers does not support a directory handle. Use write-through
// for the same-volume rename, after syncing the staged file's data.
func syncDirectory(string) error { return nil }

func commitSnapshot(from, to string) error {
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
	return windows.MoveFileEx(old, target, windows.MOVEFILE_WRITE_THROUGH)
}

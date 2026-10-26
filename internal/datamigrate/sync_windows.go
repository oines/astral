package datamigrate

import "golang.org/x/sys/windows"

func replaceFile(source, target string) error {
	s, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(s, t, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// Individual snapshot files and manifests are flushed before rename. Windows
// does not expose portable directory fsync through os.File.Sync.
func syncDir(string) error { return nil }

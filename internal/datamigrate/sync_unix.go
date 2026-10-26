//go:build !windows

package datamigrate

import "os"

func replaceFile(source, target string) error { return os.Rename(source, target) }

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

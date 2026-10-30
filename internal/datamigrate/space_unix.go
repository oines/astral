//go:build darwin || linux

package datamigrate

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func availableSpace(path string) (diskSpace, error) {
	var stat unix.Stat_t
	var fs unix.Statfs_t
	if err := unix.Stat(path, &stat); err != nil {
		return diskSpace{}, err
	}
	if err := unix.Statfs(path, &fs); err != nil {
		return diskSpace{}, err
	}
	return diskSpace{volume: fmt.Sprint(stat.Dev), available: uint64(fs.Bavail) * uint64(fs.Bsize)}, nil
}

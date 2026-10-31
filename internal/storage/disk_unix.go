//go:build darwin || linux

package storage

import "golang.org/x/sys/unix"

func diskUsage(path string) (total, used, available int64, err error) {
	var fs unix.Statfs_t
	if err = unix.Statfs(path, &fs); err != nil {
		return
	}
	total = int64(fs.Blocks) * int64(fs.Bsize)
	used = int64(fs.Blocks-fs.Bfree) * int64(fs.Bsize)
	available = int64(fs.Bavail) * int64(fs.Bsize)
	return
}

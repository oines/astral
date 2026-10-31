package storage

import "golang.org/x/sys/windows"

func diskUsage(path string) (total, used, available int64, err error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	var a, t, f uint64
	err = windows.GetDiskFreeSpaceEx(name, &a, &t, &f)
	return int64(t), int64(t - f), int64(a), err
}

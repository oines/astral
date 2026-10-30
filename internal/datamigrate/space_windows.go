package datamigrate

import (
	"golang.org/x/sys/windows"
	"strings"
)

func availableSpace(path string) (diskSpace, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return diskSpace{}, err
	}
	volume := make([]uint16, 32768)
	if err = windows.GetVolumePathName(name, &volume[0], uint32(len(volume))); err != nil {
		return diskSpace{}, err
	}
	var available, total, free uint64
	if err = windows.GetDiskFreeSpaceEx(name, &available, &total, &free); err != nil {
		return diskSpace{}, err
	}
	identity := windows.UTF16ToString(volume)
	guid := make([]uint16, 32768)
	if err = windows.GetVolumeNameForVolumeMountPoint(&volume[0], &guid[0], uint32(len(guid))); err == nil {
		identity = windows.UTF16ToString(guid)
	}
	return diskSpace{volume: strings.ToLower(identity), available: available}, nil
}

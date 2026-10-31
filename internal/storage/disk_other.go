//go:build !darwin && !linux && !windows

package storage

import "errors"

func diskUsage(string) (int64, int64, int64, error) {
	return 0, 0, 0, errors.New("disk capacity unavailable on this platform")
}

//go:build darwin || linux

package harness

import (
	"golang.org/x/sys/unix"
	"testing"
)

func successReplayCPU(t *testing.T) int64 {
	t.Helper()
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return usage.Utime.Sec*1e9 + int64(usage.Utime.Usec)*1000 + usage.Stime.Sec*1e9 + int64(usage.Stime.Usec)*1000
}

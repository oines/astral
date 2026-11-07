package perf

import (
	"fmt"
	"os"
	goruntime "runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Darwin sys/resource.h rusage_info_v2 is the UUID followed by eighteen uint64
// counters. PIDRUSAGE is proc_info call 9, and the V2 flavor is 2. Keeping the
// exact SDK layout here avoids cgo or an external sampling process.
type performanceDarwinRusageV2 struct {
	UUID     [16]byte
	Counters [18]uint64
}

func readOSResources(value *Sample) {
	value.IOMethod = "Darwin proc_info(PIDRUSAGE=9,RUSAGE_INFO_V2=2), ri_diskio_bytesread/ri_diskio_byteswritten"
	var usage performanceDarwinRusageV2
	if size := unsafe.Sizeof(usage); size != 160 {
		value.IOUnavailable = fmt.Sprintf("unexpected rusage_info_v2 layout: %d bytes, want 160", size)
		return
	}
	result, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, 9, uintptr(os.Getpid()), 2, 0, uintptr(unsafe.Pointer(&usage)), 0)
	goruntime.KeepAlive(&usage)
	if errno != 0 {
		value.IOUnavailable = fmt.Sprintf("proc_pid_rusage syscall: %v", errno)
		return
	}
	if result != 0 || usage.Counters[6] == 0 || usage.Counters[8] == 0 {
		value.IOUnavailable = fmt.Sprintf("invalid proc_pid_rusage result/layout: result=%d resident=%d process_start=%d", result, usage.Counters[6], usage.Counters[8])
		return
	}
	value.ResidentAvailable, value.ResidentBytes = true, usage.Counters[6]
	value.IOAvailable = true
	value.DiskReadBytes, value.DiskWriteBytes = usage.Counters[16], usage.Counters[17]
}

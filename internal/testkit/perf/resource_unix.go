//go:build darwin || linux

// Package perf provides resource measurements for offline tests. No collector
// forces garbage collection or retains application request data.
package perf

import (
	goruntime "runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Sample contains process-lifetime counters at one endpoint. Availability flags
// distinguish unsupported or failed OS measurements from valid zero counters.
type Sample struct {
	CapturedAt         time.Time `json:"captured_at"`
	CPUNS              int64     `json:"cpu_ns"`
	CPUAvailable       bool      `json:"cpu_available"`
	CPUUnavailable     string    `json:"cpu_unavailable,omitempty"`
	TotalAlloc         uint64    `json:"total_alloc_bytes"`
	HeapAlloc          uint64    `json:"heap_alloc_bytes"`
	HeapInuse          uint64    `json:"heap_inuse_bytes"`
	HeapSys            uint64    `json:"heap_sys_bytes"`
	Sys                uint64    `json:"go_sys_bytes"`
	NumGC              uint32    `json:"gc_cycles"`
	NumForcedGC        uint32    `json:"forced_gc_cycles"`
	Goroutines         int       `json:"goroutines"`
	PeakRSSBytes       uint64    `json:"peak_rss_bytes"`
	PeakRSSAvailable   bool      `json:"peak_rss_available"`
	ResidentBytes      uint64    `json:"resident_bytes"`
	ResidentAvailable  bool      `json:"resident_available"`
	IOMethod           string    `json:"io_method"`
	IOAvailable        bool      `json:"io_available"`
	IOUnavailable      string    `json:"io_unavailable,omitempty"`
	DiskReadBytes      uint64    `json:"disk_read_bytes"`
	DiskWriteBytes     uint64    `json:"disk_write_bytes"`
	LogicalIOAvailable bool      `json:"logical_io_available"`
	LogicalReadBytes   uint64    `json:"logical_read_bytes"`
	LogicalWriteBytes  uint64    `json:"logical_write_bytes"`
}

// Read captures current Go and OS process counters without forcing GC.
func Read() Sample {
	value := Sample{CapturedAt: time.Now(), Goroutines: goruntime.NumGoroutine()}
	var memory goruntime.MemStats
	goruntime.ReadMemStats(&memory)
	value.TotalAlloc, value.HeapAlloc, value.HeapInuse = memory.TotalAlloc, memory.HeapAlloc, memory.HeapInuse
	value.HeapSys, value.Sys, value.NumGC, value.NumForcedGC = memory.HeapSys, memory.Sys, memory.NumGC, memory.NumForcedGC
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		value.CPUUnavailable = err.Error()
	} else {
		value.CPUAvailable = true
		value.CPUNS = int64(usage.Utime.Sec)*1e9 + int64(usage.Utime.Usec)*1000 + int64(usage.Stime.Sec)*1e9 + int64(usage.Stime.Usec)*1000
		value.PeakRSSAvailable = true
		value.PeakRSSBytes = uint64(usage.Maxrss)
		if goruntime.GOOS == "linux" {
			value.PeakRSSBytes *= 1024
		}
	}
	readOSResources(&value)
	return value
}

// Difference reports interval counters and endpoint memory. Peak RSS remains
// a process-lifetime high-water mark, never a window-only maximum.
func Difference(before, after Sample) map[string]any {
	value := map[string]any{
		"wall_ns":           after.CapturedAt.Sub(before.CapturedAt).Nanoseconds(),
		"cpu_available":     before.CPUAvailable && after.CPUAvailable,
		"total_alloc_bytes": after.TotalAlloc - before.TotalAlloc,
		"heap_alloc_before": before.HeapAlloc, "heap_alloc_after": after.HeapAlloc,
		"heap_inuse_before": before.HeapInuse, "heap_inuse_after": after.HeapInuse,
		"gc_cycles": after.NumGC - before.NumGC, "forced_gc_cycles": after.NumForcedGC - before.NumForcedGC,
		"goroutines_before": before.Goroutines, "goroutines_after": after.Goroutines,
		"peak_rss_available": after.PeakRSSAvailable, "peak_rss_bytes": after.PeakRSSBytes,
		"peak_rss_scope": "process lifetime high-water; not a window-only maximum or a difference",
		"io_method":      after.IOMethod, "io_available": before.IOAvailable && after.IOAvailable,
		"logical_io_available": before.LogicalIOAvailable && after.LogicalIOAvailable,
	}
	if before.CPUAvailable && after.CPUAvailable {
		value["cpu_ns"] = after.CPUNS - before.CPUNS
	} else {
		value["cpu_unavailable"] = strings.TrimSpace(before.CPUUnavailable + " " + after.CPUUnavailable)
	}
	if before.IOAvailable && after.IOAvailable && after.DiskReadBytes >= before.DiskReadBytes && after.DiskWriteBytes >= before.DiskWriteBytes {
		value["disk_read_bytes"], value["disk_write_bytes"] = after.DiskReadBytes-before.DiskReadBytes, after.DiskWriteBytes-before.DiskWriteBytes
	} else {
		value["io_available"] = false
		value["io_unavailable"] = strings.TrimSpace(before.IOUnavailable + " " + after.IOUnavailable + " (both monotonic endpoint samples required)")
	}
	if before.LogicalIOAvailable && after.LogicalIOAvailable && after.LogicalReadBytes >= before.LogicalReadBytes && after.LogicalWriteBytes >= before.LogicalWriteBytes {
		value["logical_read_bytes"], value["logical_write_bytes"] = after.LogicalReadBytes-before.LogicalReadBytes, after.LogicalWriteBytes-before.LogicalWriteBytes
	} else {
		value["logical_io_available"] = false
	}
	return value
}

package perf

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func readOSResources(value *Sample) {
	value.IOMethod = "Linux /proc/self/io read_bytes/write_bytes and rchar/wchar"
	raw, err := os.ReadFile("/proc/self/io")
	if err != nil {
		value.IOUnavailable = err.Error()
		return
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, text, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		number, err := strconv.ParseUint(strings.TrimSpace(text), 10, 64)
		if err != nil {
			value.IOUnavailable = fmt.Sprintf("invalid /proc/self/io field %s: %v", key, err)
			return
		}
		values[key] = number
	}
	for _, key := range []string{"read_bytes", "write_bytes", "rchar", "wchar"} {
		if _, ok := values[key]; !ok {
			value.IOUnavailable = "missing /proc/self/io field: " + key
			return
		}
	}
	value.IOAvailable, value.LogicalIOAvailable = true, true
	value.DiskReadBytes, value.DiskWriteBytes = values["read_bytes"], values["write_bytes"]
	value.LogicalReadBytes, value.LogicalWriteBytes = values["rchar"], values["wchar"]
	if raw, err := os.ReadFile("/proc/self/statm"); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) > 1 {
			if pages, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				value.ResidentAvailable, value.ResidentBytes = true, pages*uint64(os.Getpagesize())
			}
		}
	}
}

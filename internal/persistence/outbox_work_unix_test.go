//go:build darwin || linux

package persistence

import (
	"encoding/json"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func outboxCPUTime(t *testing.T) int64 {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return int64(usage.Utime.Sec+usage.Stime.Sec)*int64(time.Second) + int64(usage.Utime.Usec+usage.Stime.Usec)*int64(time.Microsecond)
}

// A bounded, offline read/CPU audit. Ordinary CI retains the functional and
// unchanged-WAL tests without repeatedly collecting timing observations.
func TestOutboxWorkReadMeasurement(t *testing.T) {
	if os.Getenv("ASTRAL_OUTBOX_WORK_BENCH") != "1" {
		t.Skip("explicit bounded outbox read measurement")
	}
	s := outboxWorkFixture(t)
	var results []map[string]any
	const rounds = 5
	for _, legacy := range []bool{true, false} {
		runtime.GC()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		cpuBefore := outboxCPUTime(t)
		started := time.Now()
		count := 0
		for range rounds {
			records, err := readOutboxWork(t.Context(), s, legacy)
			if err != nil {
				t.Fatal(err)
			}
			count = len(records)
			pending := 0
			for _, r := range records {
				if r.Status == "pending" && r.ID == "pending" {
					pending++
				}
			}
			if pending != 1 || legacy && count != 1436 || !legacy && count != 1 {
				t.Fatalf("unexpected work: legacy=%v records=%d pending=%d", legacy, count, pending)
			}
		}
		elapsed := time.Since(started)
		cpu := outboxCPUTime(t) - cpuBefore
		runtime.ReadMemStats(&after)
		results = append(results, map[string]any{"legacy": legacy, "rounds": rounds, "accepted_history": 1435, "pending": 1, "returned_records_per_read": count,
			"elapsed_ns": elapsed.Nanoseconds(), "process_cpu_ns": cpu, "total_alloc_bytes": after.TotalAlloc - before.TotalAlloc, "mallocs": after.Mallocs - before.Mallocs})
	}
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(raw))
	if output := os.Getenv("ASTRAL_OUTBOX_WORK_RESULT"); output != "" {
		if err = os.WriteFile(output, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

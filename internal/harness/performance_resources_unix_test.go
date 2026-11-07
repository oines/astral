//go:build darwin || linux

package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oines/astral/internal/testkit/perf"
)

// Complete m.Run and fixture measurements stay in this package; samplers are
// shared with other offline test packages without changing their metric scope.
type performanceResourceSample = perf.Sample

type performanceFileSample struct {
	Bytes          int64 `json:"bytes"`
	ModifiedUnixNS int64 `json:"modified_unix_ns"`
}

type performanceOperationSample struct {
	Count     int64 `json:"count"`
	JSONBytes int64 `json:"json_bytes"`
}

type performanceFixtureSample struct {
	CapturedAt          time.Time                             `json:"captured_at"`
	Directory           string                                `json:"directory"`
	Files               map[string]performanceFileSample      `json:"files"`
	DatabaseBytes       int64                                 `json:"database_bytes"`
	WALBytes            int64                                 `json:"wal_bytes"`
	SHMBytes            int64                                 `json:"shm_bytes"`
	Operations          performanceOperationSample            `json:"operations"`
	ByKind              map[string]performanceOperationSample `json:"operations_by_kind"`
	OperationsAvailable bool                                  `json:"operations_available"`
	Errors              []string                              `json:"errors,omitempty"`
}

type performanceFixtureRecord struct {
	Test   string                   `json:"test"`
	Before performanceFixtureSample `json:"before"`
	After  performanceFixtureSample `json:"after"`
}

var performanceOutput string
var performanceFixtures struct {
	sync.Mutex
	values []performanceFixtureRecord
}

func performanceResources(t *testing.T) performanceResourceSample {
	t.Helper()
	return performanceReadResources()
}

func performanceReadResources() performanceResourceSample {
	return perf.Read()
}

func performanceDifference(before, after performanceResourceSample) map[string]any {
	return perf.Difference(before, after)
}

func performanceFixtureSnapshot(t *testing.T, f *fixture) performanceFixtureSample {
	t.Helper()
	value := performanceFixtureSample{CapturedAt: time.Now(), Directory: f.directory,
		Files: map[string]performanceFileSample{}, ByKind: map[string]performanceOperationSample{}}
	err := filepath.WalkDir(f.directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".db") && !strings.HasSuffix(name, ".db-wal") && !strings.HasSuffix(name, ".db-shm") {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("database accounting requires a regular file: %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(f.directory, path)
		if err != nil {
			return err
		}
		value.Files[filepath.ToSlash(relative)] = performanceFileSample{Bytes: info.Size(), ModifiedUnixNS: info.ModTime().UnixNano()}
		switch {
		case strings.HasSuffix(name, "-wal"):
			value.WALBytes += info.Size()
		case strings.HasSuffix(name, "-shm"):
			value.SHMBytes += info.Size()
		default:
			value.DatabaseBytes += info.Size()
		}
		return nil
	})
	if err != nil {
		value.Errors = append(value.Errors, "file snapshot: "+err.Error())
	}
	// Read only the Host's own operations table. This does not inspect Core's
	// private tables, checkpoint its WAL, or change the fixture's database pool.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(f.store.Directory, "astral.db"))+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err == nil {
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var rows *sql.Rows
		rows, err = db.QueryContext(ctx, `SELECT kind,count(*),coalesce(sum(length(CAST(data AS BLOB))),0) FROM operations GROUP BY kind ORDER BY kind`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var kind string
				var sample performanceOperationSample
				if err = rows.Scan(&kind, &sample.Count, &sample.JSONBytes); err != nil {
					break
				}
				value.ByKind[kind] = sample
				value.Operations.Count += sample.Count
				value.Operations.JSONBytes += sample.JSONBytes
			}
			if err == nil {
				err = rows.Err()
			}
		}
	}
	if err != nil {
		value.Errors = append(value.Errors, "Host operations snapshot: "+err.Error())
	} else {
		value.OperationsAvailable = true
	}
	return value
}

func performanceTrackFixture(t *testing.T, f *fixture) {
	t.Helper()
	if performanceOutput == "" {
		return
	}
	before := performanceFixtureSnapshot(t, f)
	t.Cleanup(func() {
		// Registered at the end of newFixture, so LIFO cleanup takes this sample
		// before the fixture closes SQLite and potentially removes its WAL.
		after := performanceFixtureSnapshot(t, f)
		performanceFixtures.Lock()
		performanceFixtures.values = append(performanceFixtures.values, performanceFixtureRecord{Test: t.Name(), Before: before, After: after})
		performanceFixtures.Unlock()
	})
}

func performanceCreateOutput(raw string) (string, error) {
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("ASTRAL_PERFORMANCE_OUTPUT must be an absolute new directory")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filepath.Clean(raw)))
	if err != nil {
		return "", err
	}
	path := filepath.Join(parent, filepath.Base(raw))
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, forbidden := range []string{filepath.Join(home, "Library", "Application Support", "astral"), filepath.Join(home, ".local", "share", "astral"), "/var/lib/astral"} {
		checked, root := path, filepath.Clean(forbidden)
		if goruntime.GOOS == "darwin" {
			checked, root = strings.ToLower(checked), strings.ToLower(root)
		}
		relative, err := filepath.Rel(root, checked)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("production output path is forbidden: %s", path)
		}
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return "", err
	}
	return path, nil
}

func performanceWriteJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(raw, '\n'))
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func TestMain(m *testing.M) {
	raw := os.Getenv("ASTRAL_PERFORMANCE_OUTPUT")
	owner := os.Getenv("ASTRAL_PERFORMANCE_OWNER_PID")
	if raw == "" || owner != "" && owner != strconv.Itoa(os.Getpid()) {
		os.Exit(m.Run())
	}
	var err error
	performanceOutput, err = performanceCreateOutput(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "performance resources:", err)
		os.Exit(1)
	}
	// Offline receiver subprocesses inherit the selected test environment but
	// must not create or overwrite their parent's evidence directory.
	if err = os.Setenv("ASTRAL_PERFORMANCE_OWNER_PID", strconv.Itoa(os.Getpid())); err != nil {
		fmt.Fprintln(os.Stderr, "performance resources:", err)
		os.Exit(1)
	}
	profileEnabled := os.Getenv("ASTRAL_PERFORMANCE_PROFILE") == "1"
	var profile *os.File
	if profileEnabled {
		profile, err = os.OpenFile(filepath.Join(performanceOutput, "cpu.pprof"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			err = pprof.StartCPUProfile(profile)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "performance resources CPU profile:", err)
			os.Exit(1)
		}
	}
	before := performanceReadResources()
	started := time.Now()
	code := m.Run()
	wall := time.Since(started).Nanoseconds()
	after := performanceReadResources()
	if profile != nil {
		pprof.StopCPUProfile()
		if err = profile.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "performance resources CPU profile:", err)
			code = 1
		}
	}
	// The natural sample and complete m.Run interval are finalized first.
	// This collection is only a retained-heap diagnostic after all test cleanup.
	goruntime.GC()
	postGC := performanceReadResources()
	difference := performanceDifference(before, after)
	difference["wall_ns"] = wall
	result := map[string]any{
		"schema": "astral-performance-resources-v1", "success": code == 0, "exit_code": code,
		"scope": "complete m.Run including setup, test bodies, assertions, instrumentation and fixture teardown; not warm request latency",
		"pid":   os.Getpid(), "os": goruntime.GOOS, "architecture": goruntime.GOARCH, "go_version": goruntime.Version(),
		"profiles_enabled": profileEnabled, "profile_note": "profile runs have sampling overhead and must be compared separately from ordinary measurement runs",
		"before": before, "after": after, "difference": difference,
		"post_gc": postGC, "post_gc_heap_alloc": postGC.HeapAlloc,
		"post_gc_scope": "one forced GC after m.Run and CPU profiling; excluded from natural samples and measured m.Run interval",
		"io_note":       "OS-attributed process storage bytes; not device NAND writes. Logical syscall bytes, retained DB/WAL lengths and JSON payload sizes are separate metrics",
	}
	performanceFixtures.Lock()
	fixtures := append([]performanceFixtureRecord{}, performanceFixtures.values...)
	performanceFixtures.Unlock()
	for name, value := range map[string]any{
		"performance.json": result,
		"fixtures.json":    map[string]any{"schema": "astral-performance-fixtures-v1", "scope": "DB/WAL/SHM lengths and Host operations before fixture teardown; file length is not write volume", "fixtures": fixtures},
	} {
		if err := performanceWriteJSON(filepath.Join(performanceOutput, name), value); err != nil {
			fmt.Fprintln(os.Stderr, "performance resources:", err)
			code = 1
		}
	}
	if profileEnabled {
		file, err := os.OpenFile(filepath.Join(performanceOutput, "heap.pprof"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			err = pprof.WriteHeapProfile(file)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "performance resources heap profile:", err)
			code = 1
		}
	}
	os.Exit(code)
}

func TestPerformanceResourceSampler(t *testing.T) {
	if os.Getenv("ASTRAL_PERFORMANCE_SELF_CHECK") != "1" {
		t.Skip("set ASTRAL_PERFORMANCE_SELF_CHECK=1 for a short sampler check")
	}
	before := performanceResources(t)
	if !before.CPUAvailable || !before.PeakRSSAvailable || before.PeakRSSBytes == 0 {
		t.Fatalf("process CPU/RSS unavailable: %+v", before)
	}
	if !before.IOAvailable {
		t.Fatalf("OS byte accounting unavailable: %s", before.IOUnavailable)
	}
	if goruntime.GOOS == "darwin" && (!before.ResidentAvailable || before.ResidentBytes == 0) {
		t.Fatalf("Darwin rusage struct did not expose a resident size: %+v", before)
	}
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i)
	}
	file, err := os.CreateTemp(t.TempDir(), "sampler-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	after := performanceResources(t)
	goruntime.KeepAlive(data)
	difference := performanceDifference(before, after)
	if after.TotalAlloc < before.TotalAlloc+uint64(len(data)) || after.CPUNS < before.CPUNS || difference["io_available"] != true {
		t.Fatalf("resource counters did not stay monotonic: %#v", difference)
	}
	// Physical attribution may legitimately be zero on a cached or deferred write.
	// Check availability/monotonicity, never invent a minimum disk write volume.
	if goruntime.GOOS == "linux" && (!after.LogicalIOAvailable || after.LogicalWriteBytes < before.LogicalWriteBytes+uint64(len(data))) {
		t.Fatalf("Linux logical write counter omitted the fixture write: before=%+v after=%+v", before, after)
	}
	t.Logf("PERFORMANCE_RESOURCE_SELF_CHECK %+v", difference)
}

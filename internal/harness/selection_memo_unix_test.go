//go:build darwin || linux

package harness

import (
	"context"
	"encoding/json"
	"os"
	goruntime "runtime"
	"syscall"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func selectionCPUTime(t *testing.T) int64 {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return int64(usage.Utime.Sec+usage.Stime.Sec)*int64(time.Second) + int64(usage.Utime.Usec+usage.Stime.Usec)*int64(time.Microsecond)
}

func legacyColdChildMetadata(ctx context.Context, s *Service, record domain.Session) error {
	state, err := s.CoreStore.LoadState(ctx, record.ID)
	if err != nil {
		return err
	}
	var settings subagentSettings
	if err := s.Store.Get(ctx, "subagent_settings", record.ID, &settings); err != nil {
		return err
	}
	configuration, err := s.configurationAtState(ctx, record.ID, state)
	if err != nil {
		return err
	}
	if !settings.Started || !jsonEqual(configuration.Selection, settings.Configuration.Selection) {
		return core.ErrNotContinuable
	}
	var activation subagentActivation
	if err := s.Store.Maintenance(ctx, "subagent_activation", record.ID, &activation); err != nil && !domain.IsMissing(err) {
		return err
	}
	return nil
}

// This measures the old hot poll's metadata portion, not its report delivery:
// two settings body reads, a complete Configuration clone and idle Snapshots.
func legacyHotChildMetadata(ctx context.Context, r *runtime) error {
	var settings subagentSettings
	if err := r.service.Store.Get(ctx, "subagent_settings", r.id, &settings); err != nil {
		return err
	}
	if !jsonEqual(r.core.Load().Configuration().Selection, settings.Configuration.Selection) {
		return core.ErrNotContinuable
	}
	if err := r.service.Store.Get(ctx, "subagent_settings", r.id, &settings); err != nil {
		return err
	}
	if !settings.Started {
		return core.ErrNotContinuable
	}
	_ = r.core.Load().Snapshot()
	_ = r.core.Load().Snapshot()
	return nil
}

// Same SQLite fixture and executable, three consecutive rounds for each mode.
// No provider is called during measurement. The legacy Summary mode forces
// the former full-read policy; child modes retain the corresponding metadata
// operations explicitly, without claiming to replay the complete old worker.
func TestConfigurationSelectionReadMeasurement(t *testing.T) {
	if os.Getenv("ASTRAL_SELECTION_MEMO_BENCH") != "1" {
		t.Skip("explicit bounded configuration read measurement")
	}
	f, record, configurationBytes, counted := largeSelectionFixture(t)
	modelCalls := f.count()
	const queries = 5
	var results []map[string]any
	for _, scope := range []string{"cold_summary", "cold_child_admission", "hot_child_metadata"} {
		var r *runtime
		if scope == "hot_child_metadata" {
			var err error
			r, err = f.service.open(f.ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, legacy := range []bool{true, false} {
			f.service.selections = nil
			f.service.childSettings = nil
			counted.resetReads()
			goruntime.GC()
			goruntime.GC()
			for round := range 3 {
				var before, after goruntime.MemStats
				goruntime.ReadMemStats(&before)
				readsBefore := counted.configReads.Load()
				cpuBefore := selectionCPUTime(t)
				started := time.Now()
				for range queries {
					var err error
					switch scope {
					case "cold_summary":
						if legacy {
							delete(f.service.selections, record.ID)
						}
						_, err = f.service.Summary(f.ctx, record.ID)
					case "cold_child_admission":
						if legacy {
							err = legacyColdChildMetadata(f.ctx, f.service, record)
						} else {
							var state core.RuntimeState
							state, err = f.service.CoreStore.LoadState(f.ctx, record.ID)
							if err == nil {
								var needed bool
								needed, err = f.service.subagentNeedsRuntime(f.ctx, record, state)
								if needed {
									t.Fatal("settled child unexpectedly needed execution")
								}
							}
						}
					case "hot_child_metadata":
						if legacy {
							err = legacyHotChildMetadata(f.ctx, r)
						} else {
							err = r.syncSubagentModel(f.ctx)
							if err == nil {
								err = r.startSubagentLocked(f.ctx)
							}
							_ = r.core.Load().State()
							_ = r.core.Load().State()
						}
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				elapsed, cpu := time.Since(started), selectionCPUTime(t)-cpuBefore
				goruntime.ReadMemStats(&after)
				reads := counted.configReads.Load() - readsBefore
				if !legacy && (round == 0 && reads != 1 || round > 0 && reads != 0) {
					t.Fatalf("unchanged configuration was not decoded just once: scope=%s round=%d reads=%d", scope, round, reads)
				}
				if scope == "hot_child_metadata" && !legacy && round > 0 && (after.TotalAlloc-before.TotalAlloc)/queries >= uint64(configurationBytes) {
					t.Fatal("hot metadata polling copied at least a complete configuration per query")
				}
				results = append(results, map[string]any{"scope": scope, "legacy": legacy, "round": round + 1, "queries": queries,
					"configuration_bytes": configurationBytes, "full_configuration_reads": reads, "alloc_bytes_per_query": (after.TotalAlloc - before.TotalAlloc) / queries,
					"process_cpu_ns_per_query": cpu / queries, "wall_ns_per_query": elapsed.Nanoseconds() / queries})
			}
		}
	}
	if f.count() != modelCalls {
		t.Fatal("metadata measurement dispatched a model")
	}
	bytes, _ := json.MarshalIndent(results, "", "  ")
	t.Log(string(bytes))
	if output := os.Getenv("ASTRAL_SELECTION_MEMO_RESULT"); output != "" {
		if err := os.WriteFile(output, bytes, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

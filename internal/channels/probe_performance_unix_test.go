//go:build darwin || linux

package channels

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
	"github.com/oines/astral/internal/testkit/perf"
)

type probePerformanceFile struct {
	Bytes          int64 `json:"bytes"`
	ModifiedUnixNS int64 `json:"modified_unix_ns"`
}

func probePerformanceFiles(t *testing.T, directory string) map[string]probePerformanceFile {
	t.Helper()
	files := map[string]probePerformanceFile{}
	if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() || !(strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".db-wal") || strings.HasSuffix(name, ".db-shm")) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = probePerformanceFile{Bytes: info.Size(), ModifiedUnixNS: info.ModTime().UnixNano()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func probePerformanceJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(append(raw, '\n'))
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

// TestProbePerformance uses only generated identities, synthetic text and the
// local testkit provider/Telegram endpoints. No real account, request body,
// credential or captured conversation is written into the evidence artifacts.
func TestProbePerformance(t *testing.T) {
	if os.Getenv("ASTRAL_PROBE_PERFORMANCE") != "1" {
		t.Skip("set ASTRAL_PROBE_PERFORMANCE=1 and ASTRAL_PERFORMANCE_OUTPUT for an explicit Probe measurement")
	}
	steps := 100
	if raw := os.Getenv("ASTRAL_PROBE_PERFORMANCE_STEPS"); raw != "" {
		var err error
		steps, err = strconv.Atoi(raw)
		if err != nil || steps != 10 && steps != 100 {
			t.Fatal("ASTRAL_PROBE_PERFORMANCE_STEPS must be 10 (smoke) or 100")
		}
	}
	mode := os.Getenv("ASTRAL_PROBE_PERFORMANCE_MODE")
	if mode == "" {
		mode = "success"
	}
	if mode != "success" && mode != "model_failure" {
		t.Fatal("ASTRAL_PROBE_PERFORMANCE_MODE must be success or model_failure")
	}
	selectionMode := os.Getenv("ASTRAL_PROBE_PERFORMANCE_SELECTION")
	if selectionMode == "" {
		selectionMode = "session"
	}
	if selectionMode != "default" && selectionMode != "session" && selectionMode != "override" {
		t.Fatal("ASTRAL_PROBE_PERFORMANCE_SELECTION must be default, session or override")
	}
	output := os.Getenv("ASTRAL_PERFORMANCE_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("ASTRAL_PERFORMANCE_OUTPUT must be an absolute new private output directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	var requests, mainRequests, wrongModel, wrongReasoning, wrongTools, leakedInput atomic.Int64
	expectedModel, expectedEffort := "fixture", ""
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		requests.Add(1)
		tools := string(testkit.JSON(request["tools"]))
		if !strings.Contains(tools, `"name":"decide"`) {
			mainRequests.Add(1)
			return testkit.Text("Synthetic unexpected main-Session request.")
		}
		if request["model"] != expectedModel {
			wrongModel.Add(1)
		}
		effort, _ := request["reasoning_effort"].(string)
		if effort != expectedEffort {
			wrongReasoning.Add(1)
		}
		functions, _ := request["tools"].([]any)
		if len(functions) != 1 || request["tool_choice"] != "auto" {
			wrongTools.Add(1)
		}
		if strings.Contains(string(testkit.JSON(request["messages"])), "SYNTHETIC_PRIVATE_INPUT_MUST_BE_FILTERED") {
			leakedInput.Add(1)
		}
		if mode == "model_failure" {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Synthetic Probe model quota failure.", "code": "insufficient_quota"}})
			})
		}
		return testkit.Tool("decide", map[string]bool{"participate": false})
	})
	if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	p.Profile.Recommendations["reasoning"] = testkit.JSON(map[string]any{"efforts": []string{"low", "high"}})
	if err := p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
		t.Fatal(err)
	}
	other := p.Profile
	other.Meta, other.Name, other.ModelID = domain.Meta{}, "Synthetic Probe model B", "synthetic-probe-model-b"
	other.Overrides = p.Profile.Recommendations
	if err := p.Harness.Models.SaveProfile(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Harness.Models.Verify(p.Context, other.ID); err != nil {
		t.Fatal(err)
	}
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, connection, "-100", "")
	if selectionMode != "default" {
		if err := p.Harness.SelectModel(p.Context, binding.SessionID, other.ID, &core.ReasoningOptions{Effort: "high"}); err != nil {
			t.Fatal(err)
		}
		expectedModel, expectedEffort = other.ModelID, "high"
	}
	if selectionMode == "override" {
		p.Agent.ProbeModelID, p.Agent.ProbeReasoning = p.Profile.ID, &core.ReasoningOptions{Effort: "low"}
		if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
			t.Fatal(err)
		}
		expectedModel, expectedEffort = p.Profile.ModelID, "low"
	}
	session, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	view := core.Context{Request: core.Request{
		SystemPrompt: "Synthetic main-Session instructions are not Probe instructions.",
		Messages: []core.Message{
			{Role: core.RoleUser, ContextKind: "summary", Content: core.Text(strings.Repeat("Synthetic bounded earlier summary. ", 16))},
			{Role: core.RoleUser, ContextKind: "im.observation", Content: core.Text(strings.Repeat("Synthetic group observation for a bounded participation decision. ", 16))},
			core.User("SYNTHETIC_PRIVATE_INPUT_MUST_BE_FILTERED"),
		},
	}}
	selectionBefore, err := p.Harness.ModelSelection(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	stateBefore := session.State()
	filesBefore := probePerformanceFiles(t, p.Directory)
	requestsBefore := p.Requests.Load()
	latencies := make([]int64, 0, steps)
	var failures, participates, wrongStepRequests int
	before := perf.Read()
	for range steps {
		stepRequests := p.Requests.Load()
		started := time.Now()
		participate, callErr := s.shouldParticipate(p.Context, binding, view)
		latencies = append(latencies, time.Since(started).Nanoseconds())
		if p.Requests.Load()-stepRequests != 1 {
			wrongStepRequests++
		}
		if callErr != nil {
			failures++
		}
		if participate {
			participates++
		}
	}
	after := perf.Read()
	// Retention is observed only after the measured request window. Keep the
	// same live platform and bounded input reachable on both sides of the GC.
	runtime.GC()
	postGC := perf.Read()
	runtime.KeepAlive(view)
	runtime.KeepAlive(p)
	filesAfter := probePerformanceFiles(t, p.Directory)
	stateAfter := session.State()
	snapshotAfter := session.Snapshot()
	selectionAfter, err := p.Harness.ModelSelection(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	requestsAfter := p.Requests.Load()
	expectedFailures := 0
	if mode == "model_failure" {
		expectedFailures = steps
	}
	invariants := map[string]bool{
		"one_request_per_step":         wrongStepRequests == 0 && requestsAfter-requestsBefore == int64(steps) && requests.Load() == int64(steps),
		"expected_results":             failures == expectedFailures && participates == 0,
		"expected_model_and_reasoning": wrongModel.Load() == 0 && wrongReasoning.Load() == 0,
		"only_probe_decide_tool":       wrongTools.Load() == 0, "private_input_filtered": leakedInput.Load() == 0,
		"no_main_model_requests": mainRequests.Load() == 0, "no_platform_send": bot.sends.Load() == 0,
		"main_session_not_awakened": !snapshotAfter.Running && !snapshotAfter.Maintenance && snapshotAfter.State.CurrentAttemptID == "",
		"main_state_unchanged":      reflect.DeepEqual(stateBefore, stateAfter), "main_selection_unchanged": reflect.DeepEqual(selectionBefore, selectionAfter),
		"no_forced_gc_in_window": after.NumForcedGC == before.NumForcedGC,
	}
	success := true
	for _, valid := range invariants {
		success = success && valid
	}
	sorted := append([]int64{}, latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	probePerformanceJSON(t, filepath.Join(output, "performance.json"), map[string]any{
		"schema": "astral-performance-resources-v1", "success": success,
		"scope": "warm shouldParticipate calls including real local SDK/HTTP transfer and receiver validation; setup, assertions, file accounting and diagnostic GC excluded",
		"os":    runtime.GOOS, "architecture": runtime.GOARCH, "go_version": runtime.Version(),
		"before": before, "after": after, "difference": perf.Difference(before, after),
		"post_gc": postGC, "post_gc_heap_alloc": postGC.HeapAlloc,
		"post_gc_scope": "one forced GC after the measured window with the same synthetic platform and bounded input alive",
	})
	probePerformanceJSON(t, filepath.Join(output, "probe.json"), map[string]any{
		"schema": "astral-probe-performance-v1", "success": success, "steps": steps, "mode": mode, "selection_mode": selectionMode,
		"model": expectedModel, "reasoning_effort": expectedEffort, "requests": requests.Load(), "requests_before": requestsBefore, "requests_after": requestsAfter,
		"failed_decisions": failures, "positive_decisions": participates, "invariants": invariants,
		"wrong_step_request_counts": wrongStepRequests, "main_version_before": stateBefore.Version, "main_version_after": stateAfter.Version,
		"latency_ns":   map[string]any{"samples": latencies, "median": sorted[len(sorted)/2], "p95": sorted[(95*len(sorted)+99)/100-1], "max": sorted[len(sorted)-1]},
		"files_before": filesBefore, "files_after": filesAfter, "file_note": "only the synthetic testkit t.TempDir database/WAL/SHM; file length is separate from process IO bytes",
		"synthetic_input_bytes": len(testkit.JSON(view.Request.Messages)),
	})
	t.Logf("synthetic Probe measurement: %s", output)
	if !success {
		t.Fatal("Probe measurement invariants failed; inspect probe.json")
	}
}

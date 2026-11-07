//go:build darwin || linux

package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
)

type hostSuccessWire struct {
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
	ConsumedNS int64  `json:"body_consumed_ns"`
	Messages   int    `json:"messages"`
}

// The receiver consumes and validates every byte before producing a response.
// Its CPU and allocation overhead is included in the process measurement.
type hostSuccessReceiver struct {
	mu       sync.Mutex
	start    time.Time
	toolPath string
	wires    []hostSuccessWire
}

func (r *hostSuccessReceiver) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if err = req.Body.Close(); err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid complete request JSON")
	}
	var body struct {
		Messages []json.RawMessage `json:"messages"`
		Stream   bool              `json:"stream"`
	}
	if err = json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	if body.Stream {
		return nil, fmt.Errorf("control fixture unexpectedly enabled streaming")
	}
	sum := sha256.Sum256(raw)
	r.mu.Lock()
	r.wires = append(r.wires, hostSuccessWire{Bytes: len(raw), SHA256: hex.EncodeToString(sum[:]), ConsumedNS: time.Since(r.start).Nanoseconds(), Messages: len(body.Messages)})
	n := len(r.wires)
	r.mu.Unlock()
	reply := textReply("Successful offline Host baseline response.")
	if r.toolPath != "" && n == 1 {
		reply = toolReply("baseline-read", "read", map[string]any{"path": r.toolPath})
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(data))), Request: req}, nil
}

// This controls the complete warm Host Submit lifecycle with a real loopback
// Computer (discovery and optional read tool). Only the remote model is replaced.
// No production configuration, filesystem or external messaging is involved.
func TestHostSuccessBaseline(t *testing.T) {
	mode := os.Getenv("ASTRAL_HOST_SUCCESS_MODE")
	if mode == "" {
		t.Skip("explicit Host success baseline required")
	}
	if mode != "text" && mode != "tool" && mode != "compact" {
		t.Fatal("expected text, tool or compact mode")
	}
	output, err := filepath.Abs(os.Getenv("ASTRAL_HOST_SUCCESS_OUTPUT"))
	if err != nil || os.Getenv("ASTRAL_HOST_SUCCESS_OUTPUT") == "" {
		t.Fatal("explicit new output directory required")
	}
	if err = os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any {
		t.Error("unexpected original provider call")
		return nil
	})
	f.service.RuntimeID = "host-success-baseline"
	receiver := &hostSuccessReceiver{}
	if mode == "tool" {
		receiver.toolPath = filepath.Join(f.daemons[0].Hello.Home, "baseline-read.txt")
		if err = os.WriteFile(receiver.toolPath, []byte("A bounded local file for the real Computer read tool.\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.service.Models.HTTPClient.Transport = receiver
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "compact" {
		receiver.start = time.Now()
		for i := range 2 {
			if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: fmt.Sprintf("seed-%d", i), Text: strings.Repeat("Bounded conversation facts retained for the explicit compaction control. ", 1800)}); err != nil {
				t.Fatal(err)
			}
			f.idle(t, f.session.ID)
		}
		receiver.mu.Lock()
		receiver.wires = nil
		receiver.mu.Unlock()
	}
	stateBefore := session.State()
	sub := session.Subscribe(512)
	defer sub.Close()
	var profile *os.File
	if os.Getenv("ASTRAL_SUCCESS_PROFILE") == "1" {
		profile, err = os.OpenFile(filepath.Join(output, "cpu.pprof"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err = pprof.StartCPUProfile(profile); err != nil {
			t.Fatal(err)
		}
		defer profile.Close()
		defer pprof.StopCPUProfile()
	}
	storageBefore := performanceFixtureSnapshot(t, f)
	resourcesBefore := performanceResources(t)
	var before, after goruntime.MemStats
	goruntime.ReadMemStats(&before)
	cpuBefore := successReplayCPU(t)
	started := time.Now()
	receiver.start = started
	var compactNS int64
	if mode == "compact" {
		if err = f.service.Compact(f.ctx, f.session.ID, "Preserve the important facts of this offline control."); err != nil {
			t.Fatal(err)
		}
		compactNS = time.Since(started).Nanoseconds()
	}
	receipt, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "success-baseline-input", Text: "Complete this bounded offline baseline request."})
	if err != nil || receipt.Error != "" {
		t.Fatalf("submit: %v %s", err, receipt.Error)
	}
	var runEnd core.Event
	var toolEndNS int64
	var attemptPurposes []string
	for runEnd.Type == "" {
		select {
		case event, ok := <-sub.C:
			if !ok {
				t.Fatalf("subscription ended: %v", sub.Err())
			}
			if event.Type == core.EventAttemptStart && event.Attempt != nil {
				attemptPurposes = append(attemptPurposes, event.Attempt.Purpose)
			}
			if event.Type == core.EventToolEnd {
				toolEndNS = time.Since(started).Nanoseconds()
			}
			if event.Type == core.EventRunEnd {
				runEnd = event
			}
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
	}
	elapsed, cpuNS := time.Since(started).Nanoseconds(), successReplayCPU(t)-cpuBefore
	goruntime.ReadMemStats(&after)
	resourcesAfter := performanceResources(t)
	storageAfter := performanceFixtureSnapshot(t, f)
	if profile != nil {
		pprof.StopCPUProfile()
		_ = profile.Close()
	}
	if runEnd.Status != "completed" || runEnd.Error != "" {
		t.Fatalf("run failed: %+v", runEnd)
	}
	stateAfter := session.State()
	attempts, err := f.coreStore.ScanAttempts(f.ctx, f.session.ID, core.AttemptQuery{Reverse: true, Limit: 10, MaxBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	receiver.mu.Lock()
	wires := append([]hostSuccessWire(nil), receiver.wires...)
	receiver.mu.Unlock()
	want := 1
	if mode == "tool" || mode == "compact" {
		want = 2
	}
	var measuredAttempts int
	var summaryAttempts int
	var verifiedToolResult bool
	for _, a := range attempts.Attempts {
		if a.Version <= stateBefore.Version {
			continue
		}
		measuredAttempts++
		if a.Purpose == "compaction" {
			summaryAttempts++
		}
		if (a.Purpose != "generation" && (mode != "compact" || a.Purpose != "compaction")) || a.Outcome == nil || a.Outcome.Status != "completed" {
			t.Fatalf("not a completed ordinary request: %s", a.ID)
		}
		request, rebuildErr := core.RebuildRequest(f.ctx, f.coreStore, a.Input, 64<<20)
		if rebuildErr != nil || len(request.Messages) == 0 {
			t.Fatalf("frozen request verification: %v", rebuildErr)
		}
		for _, message := range request.Messages {
			if message.Role == core.RoleTool && message.ToolCallID == "baseline-read" && !message.IsError {
				for _, part := range message.Content {
					verifiedToolResult = verifiedToolResult || strings.Contains(part.Text, "A bounded local file for the real Computer read tool.")
				}
			}
		}
	}
	if len(wires) != want || measuredAttempts != want {
		t.Fatalf("unexpected requests/attempts: %d/%d want %d", len(wires), measuredAttempts, want)
	}
	if mode == "compact" && (summaryAttempts != 1 || stateAfter.Branch.CompactionID == stateBefore.Branch.CompactionID) {
		t.Fatal("compaction was not committed")
	}
	if stateAfter.Version <= stateBefore.Version || stateAfter.CurrentAttemptID != "" {
		t.Fatal("final request was not committed")
	}
	if mode == "tool" && !verifiedToolResult {
		t.Fatal("real Computer read result was not successfully included in the next request")
	}
	accepted, err := session.LookupInput(f.ctx, "success-baseline-input")
	if err != nil || string(accepted.State) != "applied" {
		t.Fatalf("input not applied: %v %s", err, accepted.State)
	}
	last, err := session.History().Get(f.ctx, stateAfter.Branch.LastMessageID)
	if err != nil || last.Message == nil || last.Message.Role != core.RoleAssistant || len(last.Message.Content) != 1 || last.Message.Content[0].Text != "Successful offline Host baseline response." {
		t.Fatalf("final reply mismatch: %v", err)
	}
	result := map[string]any{
		"success": true, "mode": mode, "scope": "warm Host Submit + Core + SDK + loopback Computer + full offline model body consumption + final reply/run commit",
		"wall_ns": elapsed, "cpu_ns": cpuNS, "total_alloc_bytes": after.TotalAlloc - before.TotalAlloc,
		"heap_alloc_before": before.HeapAlloc, "heap_alloc_after": after.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC,
		"forced_gc": 0, "http_calls": len(wires), "requests": wires, "attempt_purposes": attemptPurposes,
		"version_before": stateBefore.Version, "version_after": stateAfter.Version,
		"tool_end_event_observed_ns": toolEndNS,
		"compact_completed_ns":       compactNS,
		"resource_window":            performanceDifference(resourcesBefore, resourcesAfter),
		"storage_before":             storageBefore,
		"storage_after":              storageAfter,
		"limitations":                []string{"synthetic small control, not captured production context", "session and Computer initialized before timing; not a cold-start measurement", "real local Computer RPC/discovery included; remote network latency excluded", "offline receiver reads, validates and hashes full request; its overhead is included", "ToolEnd event follows result commit, not raw tool implementation return", "rebuild/digest verification after timing; production freeze and commits remain inside timing"},
	}
	// Retention is diagnosed after the measured lifecycle and request checks.
	// This forced GC never enters the hot-loop timing or allocation metrics.
	goruntime.GC()
	var retained goruntime.MemStats
	goruntime.ReadMemStats(&retained)
	result["post_gc_heap_alloc_after_verification"] = retained.HeapAlloc
	result["post_gc_heap_scope"] = "live Host/Computer after request verification; forced GC outside measured interval"
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(output, "result.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	heap, err := os.OpenFile(filepath.Join(output, "heap.pprof"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = pprof.WriteHeapProfile(heap); err != nil {
		t.Fatal(err)
	}
	_ = heap.Close()
	t.Logf("Host success baseline mode=%s total=%s cpu=%s alloc=%d HTTP=%d", mode, time.Duration(elapsed), time.Duration(cpuNS), after.TotalAlloc-before.TotalAlloc, len(wires))
}

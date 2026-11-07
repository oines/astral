package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

const (
	fallbackCompactHistory = "ORIGINAL_PUBLIC_HISTORY_7826"
	fallbackCompactTool    = "COMPLETED_LOCAL_READ_9153"
	fallbackCompactFailed  = "FAILED_SUMMARY_MUST_NOT_ENTER_CONTEXT_2671"
	fallbackCompactSummary = "Recovered summary preserves the original public historical observations."
)

// Omit synthetic usage so both the initial request and its successor exercise
// the real SDK estimator instead of trusting the small fixture usage anchor.
func fallbackCompactText(text string) any {
	reply := textReply(text).(map[string]any)
	delete(reply, "usage")
	return reply
}

func fallbackCompactGeneration(request map[string]any) bool {
	messages, _ := request["messages"].([]any)
	for _, value := range messages {
		message, _ := value.(map[string]any)
		if message["role"] == "system" {
			text, _ := message["content"].(string)
			return strings.Contains(text, hostPromptHeading)
		}
	}
	return false
}

func fallbackCompactFixture(t *testing.T, largeBackup bool) (*fixture, domain.ModelProfile, core.RuntimeState) {
	t.Helper()
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any {
		t.Error("model request escaped the offline transport")
		return nil
	})
	toolPath := filepath.Join(f.daemons[0].Hello.Home, "fallback-compaction-read.txt")
	if err := os.WriteFile(toolPath, []byte(fallbackCompactTool), 0600); err != nil {
		t.Fatal(err)
	}
	var seedTool atomic.Bool
	var seedToolCalls atomic.Int64
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		if seedTool.Load() && seedToolCalls.Add(1) == 1 {
			return modelFallbackTestResponse(request, 200, string(encode(toolReply("completed-before-compaction", "read", map[string]any{"path": toolPath})))), nil
		}
		return modelFallbackTestResponse(request, 200, string(encode(fallbackCompactText("Recorded public history and the completed local read.")))), nil
	})
	// Actual Host admission seeds two sizeable history groups, then executes a
	// real Computer read. Its result is in the recent tail retained by Compact.
	for i := range 2 {
		text := fallbackCompactHistory + strings.Repeat(" Public historical observations preserve completed work and local file facts. ", 1600)
		if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: fmt.Sprintf("compact-history-%d", i), Text: text}); err != nil {
			t.Fatal(err)
		}
		f.idle(t, f.session.ID)
	}
	seedTool.Store(true)
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "compact-completed-tool", Text: "Read the bounded local fixture before continuing."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if seedToolCalls.Load() != 2 {
		t.Fatalf("seed did not complete exactly one tool: model calls=%d", seedToolCalls.Load())
	}
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := session.InspectContext(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.EstimatedInput < 30000 || session.State().Branch.CompactionID != "" {
		t.Fatalf("seed must be substantial, uncompressed history: tokens=%d compaction=%s", view.EstimatedInput, session.State().Branch.CompactionID)
	}
	var primary domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &primary); err != nil {
		t.Fatal(err)
	}
	// The same history fits, but lies inside Core's default 16k compaction
	// reserve. A new short driving input cannot consume this 12k headroom.
	window := view.EstimatedInput + 12000
	primary.Overrides = map[string]json.RawMessage{"context_window": encode(window)}
	if err := f.service.Models.SaveProfile(f.ctx, &primary); err != nil {
		t.Fatal(err)
	}
	backup := anotherModel(t, f)
	if largeBackup {
		window += 128000
	}
	backup.Overrides = map[string]json.RawMessage{"context_window": encode(window)}
	if err := f.service.Models.SaveProfile(f.ctx, &backup); err != nil {
		t.Fatal(err)
	}
	f.agent.ModelPriority = []core.ModelSelection{{ID: primary.ID}, {ID: backup.ID}}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	// Verify the profile update still resolves the ordinary production model.
	resolved, err := f.service.Models.ResolveSelection(f.ctx, core.ModelSelection{ID: primary.ID})
	if err != nil || resolved.Model.Spec().ContextWindow != view.EstimatedInput+12000 {
		t.Fatalf("primary model did not use the calibrated window: %v", err)
	}
	return f, backup, session.State()
}

func TestModelFallbackAutomaticCompactionFailureRetriesSummaryOnNextModel(t *testing.T) {
	testModelFallbackAutomaticCompaction(t, false)
}

func TestModelFallbackAutomaticCompactionFailureSkipsSummaryForLargerModel(t *testing.T) {
	testModelFallbackAutomaticCompaction(t, true)
}

func testModelFallbackAutomaticCompaction(t *testing.T, largeBackup bool) {
	t.Helper()
	f, backup, before := fallbackCompactFixture(t, largeBackup)
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := session.History().Get(f.ctx, before.Branch.HeadID)
	if err != nil {
		t.Fatal(err)
	}
	originalJSON := string(encode(original))
	var primarySummaries, backupSummaries, backupGenerations atomic.Int64
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		decoded, raw := modelFallbackTestRequest(t, request)
		generation := fallbackCompactGeneration(decoded)
		if strings.Contains(raw, fallbackCompactFailed) {
			t.Error("failed summary diagnostics entered a later model request")
		}
		if decoded["model"] == "fixture" {
			primarySummaries.Add(1)
			if generation || !strings.Contains(raw, fallbackCompactHistory) {
				t.Error("primary must fail while summarizing the original history, before ordinary generation")
			}
			body := encode(map[string]any{"error": map[string]any{"message": "quota exhausted " + fallbackCompactFailed, "code": "insufficient_quota"}})
			return modelFallbackTestResponse(request, 402, string(body)), nil
		}
		if decoded["model"] != backup.ModelID {
			t.Errorf("unexpected candidate: %v", decoded["model"])
		}
		if !generation {
			backupSummaries.Add(1)
			if !strings.Contains(raw, fallbackCompactHistory) {
				t.Error("fallback summary lost the original uncompressed history")
			}
			return modelFallbackTestResponse(request, 200, string(encode(fallbackCompactText(fallbackCompactSummary)))), nil
		}
		backupGenerations.Add(1)
		if !strings.Contains(raw, fallbackCompactTool) || !strings.Contains(raw, "continue-after-summary-failure") {
			t.Error("recovery lost the completed tool result or original driving input")
		}
		if largeBackup && !strings.Contains(raw, fallbackCompactHistory) {
			t.Error("large backup did not receive the original uncompressed history")
		}
		if !largeBackup && !strings.Contains(raw, fallbackCompactSummary) {
			t.Error("small backup did not use its successful summary")
		}
		return modelFallbackTestResponse(request, 200, string(encode(fallbackCompactText("Finished the original task after summary failure.")))), nil
	})
	receipt, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "continue-after-summary-failure", Text: "continue-after-summary-failure: finish using the completed local read."})
	if err != nil || receipt.Error != "" {
		t.Fatalf("submit: %v %s", err, receipt.Error)
	}
	f.idle(t, f.session.ID)
	wantSummaries := int64(1)
	if largeBackup {
		wantSummaries = 0
	}
	if primarySummaries.Load() != 1 || backupSummaries.Load() != wantSummaries || backupGenerations.Load() != 1 {
		t.Fatalf("unexpected model calls: primary summaries=%d backup summaries=%d generations=%d", primarySummaries.Load(), backupSummaries.Load(), backupGenerations.Load())
	}
	page, err := session.Attempts().Scan(f.ctx, core.AttemptQuery{Limit: 100, MaxBytes: 16 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var failedSummaries, completedSummaries, completedGenerations int
	for _, attempt := range page.Attempts {
		if attempt.Version <= before.Version {
			continue
		}
		if attempt.Outcome == nil {
			t.Fatal("attempt was not finalized")
		}
		switch {
		case attempt.Purpose == "compaction" && attempt.Outcome.Status == "failed":
			failedSummaries++
		case attempt.Purpose == "compaction" && attempt.Outcome.Status == "completed":
			completedSummaries++
		case attempt.Purpose == "generation" && attempt.Outcome.Status == "completed":
			completedGenerations++
		default:
			t.Fatalf("unexpected measured attempt: purpose=%s outcome=%s", attempt.Purpose, attempt.Outcome.Status)
		}
	}
	if failedSummaries != 1 || completedSummaries != int(wantSummaries) || completedGenerations != 1 {
		t.Fatalf("attempt audit differs from wire calls: failed summaries=%d completed summaries=%d generations=%d", failedSummaries, completedSummaries, completedGenerations)
	}
	compactions, err := session.History().Scan(f.ctx, core.Query{Kind: core.EntryCompaction, Limit: 100})
	if err != nil || len(compactions.Entries) != int(wantSummaries) {
		t.Fatalf("failed summary was adopted or successful summary was lost: count=%d err=%v", len(compactions.Entries), err)
	}
	for _, entry := range compactions.Entries {
		if entry.Compaction.Summary != fallbackCompactSummary {
			t.Fatalf("unexpected committed summary: %q", entry.Compaction.Summary)
		}
	}
	stillOriginal, err := session.History().Get(f.ctx, before.Branch.HeadID)
	if err != nil || string(encode(stillOriginal)) != originalJSON {
		t.Fatalf("recovery rewrote the original archived history: %v", err)
	}
	tools, err := session.History().Scan(f.ctx, core.Query{Kind: core.EntryToolStarted, Limit: 100})
	if err != nil || len(tools.Entries) != 1 || tools.Entries[0].Tool.Invocation.Call.ID != "completed-before-compaction" {
		t.Fatalf("recovery replayed a completed tool: count=%d err=%v", len(tools.Entries), err)
	}
	input, err := session.LookupInput(f.ctx, receipt.InputID)
	if err != nil || input.State != core.InputApplied {
		t.Fatalf("original driving input was lost: %+v %v", input, err)
	}
	selection, err := f.service.ModelSelection(f.ctx, f.session.ID)
	if err != nil || selection.ID != backup.ID {
		t.Fatalf("recovery ended on the wrong model: %+v %v", selection, err)
	}
}

// Opt-in full Host lifecycle repetition. The transport retains counters only;
// historical model bodies are not retained by the fixture receiver. Resource
// measurements for the entire case are collected by the shared test runner.
func TestModelFallbackRepeatedPerformance(t *testing.T) {
	if os.Getenv("ASTRAL_FALLBACK_PERF") != "1" {
		t.Skip("set ASTRAL_FALLBACK_PERF=1 for repeated offline fallback work")
	}
	output := os.Getenv("ASTRAL_PERFORMANCE_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("ASTRAL_PERFORMANCE_OUTPUT must be an absolute output directory")
	}
	steps := 100
	if value := os.Getenv("ASTRAL_FALLBACK_PERF_STEPS"); value != "" {
		var err error
		steps, err = strconv.Atoi(value)
		if err != nil || steps < 1 || steps > 10000 {
			t.Fatal("ASTRAL_FALLBACK_PERF_STEPS must be between 1 and 10000")
		}
	}
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any {
		t.Error("model request escaped the offline performance transport")
		return nil
	})
	toolPath := filepath.Join(f.daemons[0].Hello.Home, "repeated-fallback-read.txt")
	const resultMarker = "REPEATED_FALLBACK_COMPLETED_READ_7712"
	if err := os.WriteFile(toolPath, []byte(resultMarker), 0600); err != nil {
		t.Fatal(err)
	}
	backup := anotherModel(t, f)
	for _, id := range []string{f.agent.DefaultModelID, backup.ID} {
		var profile domain.ModelProfile
		if err := f.store.Get(f.ctx, "models", id, &profile); err != nil {
			t.Fatal(err)
		}
		profile.Overrides = map[string]json.RawMessage{"context_window": encode(1000000)}
		if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
			t.Fatal(err)
		}
	}
	f.agent.ModelPriority = []core.ModelSelection{{ID: f.agent.DefaultModelID}, {ID: backup.ID}}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	var currentStep, backupThisStep, primaryCalls, backupCalls atomic.Int64
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		decoded, raw := modelFallbackTestRequest(t, request)
		if !fallbackCompactGeneration(decoded) {
			t.Error("repeated small-context control unexpectedly compacted")
		}
		if decoded["model"] == "fixture" {
			primaryCalls.Add(1)
			return modelFallbackTestResponse(request, 402, `{"error":{"message":"quota exhausted","code":"insufficient_quota"}}`), nil
		}
		if decoded["model"] != backup.ModelID {
			t.Errorf("unexpected repeated fallback model: %v", decoded["model"])
		}
		backupCalls.Add(1)
		if backupThisStep.Add(1) == 1 {
			id := fmt.Sprintf("repeated-fallback-read-%04d", currentStep.Load())
			return modelFallbackTestResponse(request, 200, string(encode(toolReply(id, "read", map[string]any{"path": toolPath})))), nil
		}
		if !strings.Contains(raw, resultMarker) {
			t.Error("continuation lost the completed tool result")
		}
		return modelFallbackTestResponse(request, 200, string(encode(fallbackCompactText("Completed this repeated fallback task.")))), nil
	})
	var samples []map[string]any
	sample := func(step int) {
		t.Helper()
		var memory goruntime.MemStats
		goruntime.ReadMemStats(&memory)
		f.service.modelFallbackMu.Lock()
		var state modelFallbackState
		err := f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state)
		_, probe := f.service.modelFallbackProbes[f.session.ID]
		f.service.modelFallbackMu.Unlock()
		if err != nil && step != 0 {
			t.Fatal(err)
		}
		if step > 0 && (fallbackArmed(state) || probe) {
			t.Fatalf("completed repetition retained active recovery: phase=%s probe=%v", state.Phase, probe)
		}
		value := map[string]any{
			"step": step, "heap_alloc_bytes": memory.HeapAlloc, "heap_inuse_bytes": memory.HeapInuse,
			"total_alloc_bytes": memory.TotalAlloc, "gc_count": memory.NumGC, "forced_gc_count": memory.NumForcedGC, "goroutines": goruntime.NumGoroutine(),
			"maintenance_json_bytes": len(encode(state)), "suppressed_runs": len(state.SuppressedRuns),
			"primary_requests": primaryCalls.Load(), "backup_requests": backupCalls.Load(),
		}
		samples = append(samples, value)
		t.Logf("FALLBACK_PERFORMANCE_SAMPLE %s", encode(value))
	}
	sample(0)
	for step := 1; step <= steps; step++ {
		currentStep.Store(int64(step))
		backupThisStep.Store(0)
		id := fmt.Sprintf("repeated-fallback-input-%04d", step)
		receipt, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: id, Text: "Read the local fixture once, then finish this bounded task."})
		if err != nil || receipt.Error != "" {
			t.Fatalf("step %d: submit: %v %s", step, err, receipt.Error)
		}
		f.idle(t, f.session.ID)
		if primaryCalls.Load() != int64(step) || backupCalls.Load() != int64(2*step) || backupThisStep.Load() != 2 {
			t.Fatalf("step %d repeated or replaced work: primary=%d backup=%d this backup=%d", step, primaryCalls.Load(), backupCalls.Load(), backupThisStep.Load())
		}
		if step%20 == 0 || step == steps {
			sample(step)
		}
	}
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, steps)
	for cursor := ""; ; {
		tools, err := session.History().Scan(f.ctx, core.Query{Kind: core.EntryToolStarted, Cursor: cursor, Limit: 100, MaxBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range tools.Entries {
			id := entry.Tool.Invocation.Call.ID
			if seen[id] {
				t.Fatalf("completed tool was executed twice: %s", id)
			}
			seen[id] = true
		}
		cursor = tools.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != steps {
		t.Fatalf("completed tools were repeated or omitted: got=%d want=%d", len(seen), steps)
	}
	result, err := os.OpenFile(filepath.Join(output, "repeated.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewEncoder(result).Encode(map[string]any{
		"success": true, "steps": steps, "samples": samples,
		"total_requests": primaryCalls.Load() + backupCalls.Load(), "primary_requests": primaryCalls.Load(), "backup_requests": backupCalls.Load(),
		"completed_tools": len(seen), "scope": "real Host Submit, Core, SDK, SQLite and loopback Computer; offline model transport retains counters only",
	})
	closeErr := result.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("write repeated performance result: %v %v", err, closeErr)
	}
}

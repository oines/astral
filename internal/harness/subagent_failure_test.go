package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	testshell "github.com/oines/astral/internal/testkit/shell"
)

const subagentFailureMarker = "CHILD_PROVIDER_AUTH_FAILURE_MARKER"

// Fail inside the real model adapter, after Core has frozen a child Attempt.
// The fixture provider still handles every ordinary parent request normally.
func subagentFailureInstall401(f *fixture) *atomic.Int32 {
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	dispatches := &atomic.Int32{}
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		if !bytes.Contains(body, []byte("You are a subagent of Session")) {
			return original.RoundTrip(request)
		}
		dispatches.Add(1)
		payload := encode(map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "invalid_api_key", "message": subagentFailureMarker}})
		return &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(payload)), Request: request}, nil
	})
	return dispatches
}

func subagentFailureWait(t *testing.T, f *fixture, id string) core.RuntimeState {
	t.Helper()
	var state core.RuntimeState
	eventually(t, f.ctx, func() bool {
		var err error
		state, err = f.coreStore.LoadState(f.ctx, id)
		return err == nil && state.LastRun != nil && state.LastRun.Status == "failed" && state.CurrentAttemptID == ""
	})
	if !strings.Contains(state.LastRun.Error, "401") || !strings.Contains(strings.ToLower(state.LastRun.Error), "credentials") {
		t.Fatalf("fixture did not persist the real provider failure: %+v", state.LastRun)
	}
	return state
}

func subagentFailureNotifications(t *testing.T, f *fixture, parent, child, run string) []core.Entry {
	t.Helper()
	var result []core.Entry
	for _, entry := range subagentTestEntries(t, f, parent) {
		if entry.Message == nil || entry.Message.ContextKind != subagentUpdateKind {
			continue
		}
		var metadata map[string]string
		if json.Unmarshal(entry.Message.Metadata, &metadata) == nil && metadata["child_session_id"] == child && metadata["run_id"] == run && metadata["run_status"] == "failed" {
			result = append(result, entry)
		}
	}
	return result
}

func subagentTerminalRunIDs(t *testing.T, f *fixture, id, status string) []string {
	t.Helper()
	var result []string
	for _, entry := range subagentTestEntries(t, f, id) {
		if entry.Run != nil && entry.Run.Status == status {
			result = append(result, entry.Run.ID)
		}
	}
	return result
}

func subagentFailureAssertNotification(t *testing.T, f *fixture, parent, child string, failed core.RuntimeState) core.Entry {
	t.Helper()
	var entries []core.Entry
	eventually(t, f.ctx, func() bool {
		entries = subagentFailureNotifications(t, f, parent, child, failed.LastRun.ID)
		return len(entries) > 0
	})
	if len(entries) != 1 {
		t.Fatalf("failed Run has %d notifications, want 1", len(entries))
	}
	entry := entries[0]
	var metadata map[string]string
	if err := json.Unmarshal(entry.Message.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	text := string(encode(entry.Message.Content))
	if metadata["run_error"] != failed.LastRun.Error || !strings.Contains(text, "failed Run") || !strings.Contains(text, failed.LastRun.ID) || !strings.Contains(text, failed.LastRun.Error) {
		t.Fatalf("parent notification lost failure status or error: metadata=%+v message=%s", metadata, text)
	}
	return entry
}

func TestSubagentFailedRunBackfillsLegacyCheckpointAndWakesIdleParentOnceAcrossRestart(t *testing.T) {
	var parentFailureRequests atomic.Int32
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if raw := string(encode(request)); strings.Contains(raw, "HTTP 401") && strings.Contains(raw, "failed Run") {
			parentFailureRequests.Add(1)
		}
		return textReply("Parent reviewed the failed child and its error.")
	})
	dispatches := subagentFailureInstall401(f)
	parent := subagentTestSeed(t, f)
	parentBefore := parent.core.Load().State().LastRun.ID
	subagentRecoveryStopWorker(f)
	child := subagentTestSpawn(t, f, parent, "failed-child-upgrade", "This request fails authentication.", false)
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	err = r.startSubagentLocked(f.ctx)
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	failed := subagentFailureWait(t, f, child.SessionID)
	var sequence uint64
	for _, entry := range subagentTestEntries(t, f, child.SessionID) {
		if entry.Sequence > sequence {
			sequence = entry.Sequence
		}
	}
	// An old Host acknowledged the entire failed Run without notifying anyone.
	// Version equality would otherwise suppress delivery forever after upgrade.
	if err := f.store.SaveMaintenance(f.ctx, "subagent_delivery", child.SessionID, "complete", map[string]any{"version": failed.Version, "sequence": sequence, "protocol": 0}); err != nil {
		t.Fatal(err)
	}
	if len(subagentFailureNotifications(t, f, parent.id, child.SessionID, failed.LastRun.ID)) != 0 {
		t.Fatal("fixture delivered the failed Run before its simulated upgrade")
	}
	f.restart(t)
	notification := subagentFailureAssertNotification(t, f, parent.id, child.SessionID, failed)
	eventually(t, f.ctx, func() bool { return parentFailureRequests.Load() == 1 })
	f.idle(t, parent.id)
	parentState, err := f.coreStore.LoadState(f.ctx, parent.id)
	if err != nil || parentState.LastRun == nil || parentState.LastRun.ID == parentBefore {
		t.Fatalf("failed child did not wake an idle parent: %+v %v", parentState.LastRun, err)
	}
	receipt := subagentRecoveryReceipt(t, f, parent.id, "subagent_run_end_"+child.SessionID+"_"+failed.LastRun.ID)
	if receipt.Kind != core.InputFollowUp {
		t.Fatalf("idle parent received failure as %s, want FollowUp", receipt.Kind)
	}
	var checkpoint struct {
		Version  uint64 `json:"version"`
		Protocol int    `json:"protocol"`
	}
	eventually(t, f.ctx, func() bool {
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == failed.Version && checkpoint.Protocol == 1
	})
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	for replay := 0; replay < 2; replay++ {
		for range 3 {
			if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
				t.Fatal(err)
			}
		}
		f.restart(t)
	}
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	f.idle(t, parent.id)
	again := subagentFailureAssertNotification(t, f, parent.id, child.SessionID, failed)
	if !bytes.Equal(encode(notification), encode(again)) || parentFailureRequests.Load() != 1 || dispatches.Load() != 1 {
		t.Fatalf("replay repeated or changed a failure notification/request: parent=%d child=%d", parentFailureRequests.Load(), dispatches.Load())
	}
}

func TestSubagentFailedRunSteersRunningParentAndKeepsItsCurrentRun(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var held atomic.Bool
	var parentSawFailure atomic.Bool
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		raw := string(encode(request))
		if strings.Contains(raw, "BUSY_PARENT_FAILURE_MARKER") && !held.Swap(true) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
		}
		if strings.Contains(raw, "HTTP 401") && strings.Contains(raw, "failed Run") {
			parentSawFailure.Store(true)
		}
		return textReply("Parent reached its next boundary and reviewed the failure.")
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	subagentFailureInstall401(f)
	parent := subagentTestSeed(t, f)
	if _, err := f.service.Submit(f.ctx, parent.id, InputRequest{ID: "busy-parent-failure", Text: "BUSY_PARENT_FAILURE_MARKER: keep working while the delegated request runs."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	busyRun := parent.core.Load().State().LastRun.ID
	child := subagentTestSpawn(t, f, parent, "failed-child-busy-parent", "Fail while the parent remains active.", false)
	failed := subagentFailureWait(t, f, child.SessionID)
	inputID := "subagent_run_end_" + child.SessionID + "_" + failed.LastRun.ID
	var receipt core.InputReceipt
	eventually(t, f.ctx, func() bool {
		var err error
		receipt, err = f.coreStore.LookupInput(f.ctx, parent.id, inputID)
		return err == nil
	})
	if receipt.Kind != core.InputSteer || !parent.core.Load().Snapshot().Running {
		t.Fatalf("active parent did not durably receive Steer while blocked: %+v", receipt)
	}
	releaseOnce.Do(func() { close(release) })
	subagentRecoveryReceipt(t, f, parent.id, inputID)
	subagentFailureAssertNotification(t, f, parent.id, child.SessionID, failed)
	eventually(t, f.ctx, func() bool { return parentSawFailure.Load() })
	f.idle(t, parent.id)
	if state := parent.core.Load().State(); state.LastRun == nil || state.LastRun.ID != busyRun {
		t.Fatalf("Steer opened a separate parent Run: before=%s after=%+v", busyRun, state.LastRun)
	}
}

func TestSubagentQueuedModelBecomesUnavailableBeforeInitializationStillNotifiesParent(t *testing.T) {
	var failureRequests atomic.Int32
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "failed Run") {
			failureRequests.Add(1)
		}
		return textReply("Parent reviewed initialization-time model unavailability.")
	})
	parent := subagentTestSeed(t, f)
	model := anotherModel(t, f)
	subagentRecoveryStopWorker(f)
	child, err := f.service.spawnSubagent(f.ctx, parent, core.Invocation{SessionID: parent.id, ExecutionID: "queued-model-disabled"}, SpawnAgentInput{Task: "Accepted while the selected model was usable.", Model: model.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Selection was valid at durable spawn admission. The resource changes
	// before any child Core exists, so its first passive creation can fail
	// before a Run event; reopening must still reach a reportable failed Run.
	disabled := false
	model.Enabled = &disabled
	if err := f.service.Models.SaveProfile(f.ctx, &model); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	var failed core.RuntimeState
	eventually(t, f.ctx, func() bool {
		var err error
		failed, err = f.coreStore.LoadState(f.ctx, child.SessionID)
		return err == nil && failed.LastRun != nil && failed.LastRun.Status == "failed" && strings.Contains(failed.LastRun.Error, "disabled")
	})
	subagentFailureAssertNotification(t, f, parent.id, child.SessionID, failed)
	eventually(t, f.ctx, func() bool { return failureRequests.Load() == 1 })
	f.idle(t, parent.id)
	if got := subagentTestRequest(t, f, parent.id); !strings.Contains(string(encode(got.Messages)), failed.LastRun.Error) {
		t.Fatal("parent woke without the asynchronous model-selection error")
	}
	initialID := "subagent_initial_" + child.SessionID
	if initial, err := f.coreStore.LookupInput(f.ctx, child.SessionID, initialID); err != nil || initial.State != core.InputPending {
		t.Fatalf("fixture no longer exposes failure before applying the initial input: %+v %v", initial, err)
	}
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	assertStable := func() {
		t.Helper()
		if runs := subagentTerminalRunIDs(t, f, child.SessionID, "failed"); len(runs) != 1 || runs[0] != failed.LastRun.ID {
			t.Fatalf("pending initial input automatically restarted a failed child: %v", runs)
		}
		if notifications := subagentFailureNotifications(t, f, parent.id, child.SessionID, failed.LastRun.ID); len(notifications) != 1 || failureRequests.Load() != 1 {
			t.Fatalf("failed initialization produced a notification storm: notifications=%d parent requests=%d", len(notifications), failureRequests.Load())
		}
		if initial, err := f.coreStore.LookupInput(f.ctx, child.SessionID, initialID); err != nil || initial.State != core.InputPending {
			t.Fatalf("reconciliation consumed an intentionally suspended initial input: %+v %v", initial, err)
		}
	}
	for restart := 0; restart < 2; restart++ {
		// Explicit cycles exercise the same activation decision as each worker
		// tick without depending on a wall-clock delay to expose a restart loop.
		for range 4 {
			if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
				t.Fatal(err)
			}
		}
		f.idle(t, parent.id)
		assertStable()
		f.restart(t)
	}
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	f.idle(t, parent.id)
	assertStable()
}

func TestSubagentCancelledRunWithPendingInitialInputDoesNotRestartOrWakeParent(t *testing.T) {
	var childRequests atomic.Int32
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "You are a subagent of Session") {
			childRequests.Add(1)
		}
		return textReply("Ordinary parent result.")
	})
	parent := subagentTestSeed(t, f)
	parentRun := parent.core.Load().State().LastRun.ID
	subagentRecoveryStopWorker(f)
	task := "A stopped initial instruction must wait for an explicit parent decision."
	child := subagentTestSpawn(t, f, parent, "cancelled-pending-initial", task, false)
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	message := core.User(task)
	message.ContextKind = "astral.subagent_instruction"
	initialID := "subagent_initial_" + child.SessionID
	if receipt, err := r.core.Load().Steer(f.ctx, core.Input{ID: initialID, Digest: digest(task), Message: message}); err != nil || receipt.State != core.InputPending {
		t.Fatalf("persist pending initial instruction: %+v %v", receipt, err)
	}
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Started = true
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	state, err := f.coreStore.LoadState(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct a process stopping after Run start but before applying its
	// initial input. Commit only public Core facts while no live handle exists.
	runID := "cancelled-before-initial-application"
	entries := []core.Entry{
		{ID: "cancelled-initial-started", RunID: runID, Kind: core.EntryRun, Run: &core.RunRecord{ID: runID, Status: "started"}},
		{ID: "cancelled-initial-terminal", RunID: runID, Kind: core.EntryRun, Run: &core.RunRecord{ID: runID, Status: "cancelled"}},
	}
	for index := range entries {
		entries[index].SessionID, entries[index].Version, entries[index].Time = child.SessionID, state.Version+1, time.Now().UTC()
	}
	if _, err := f.coreStore.Commit(f.ctx, child.SessionID, core.Commit{ID: "cancelled-initial-window", ExpectedVersion: state.Version, Entries: entries}); err != nil {
		t.Fatal(err)
	}
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	for restart := 0; restart < 2; restart++ {
		f.restart(t)
		for range 4 {
			if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
				t.Fatal(err)
			}
		}
		state, err := f.coreStore.LoadState(f.ctx, child.SessionID)
		if err != nil || state.LastRun == nil || state.LastRun.ID != runID || state.LastRun.Status != "cancelled" {
			t.Fatalf("recovery changed the cancelled terminal Run: %+v %v", state.LastRun, err)
		}
		if starts := subagentTerminalRunIDs(t, f, child.SessionID, "started"); len(starts) != 1 || starts[0] != runID {
			t.Fatalf("cancelled initial input restarted automatically: %v", starts)
		}
		if receipt, err := f.coreStore.LookupInput(f.ctx, child.SessionID, initialID); err != nil || receipt.State != core.InputPending {
			t.Fatalf("recovery consumed the cancelled initial input: %+v %v", receipt, err)
		}
		for _, entry := range subagentTestEntries(t, f, parent.id) {
			if entry.Message != nil && entry.Message.ContextKind == subagentUpdateKind && strings.Contains(string(entry.Message.Metadata), child.SessionID) {
				t.Fatal("cancelled Run notified or woke its parent")
			}
		}
		parentState, err := f.coreStore.LoadState(f.ctx, parent.id)
		if err != nil || parentState.LastRun == nil || parentState.LastRun.ID != parentRun || childRequests.Load() != 0 || f.count() != 1 {
			t.Fatalf("cancelled recovery invoked a model: parent=%+v child requests=%d all requests=%d error=%v", parentState.LastRun, childRequests.Load(), f.count(), err)
		}
	}
}

func TestSubagentMidRunFailureWakesParentWithPriorPassiveProgressAndErrorOnce(t *testing.T) {
	const progress = "MID_RUN_SUCCESSFUL_TOOL_PROGRESS_MARKER"
	secondRequest, releaseFailure := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var parentCalls, childDispatches atomic.Int32
	type observed struct{ progressCount, errorCount int }
	parentFailure := make(chan observed, 1)
	f := newFixture(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		raw := string(encode(request))
		if strings.Contains(raw, "You are a subagent of Session") {
			response := toolReply("mid-run-inspect", "exec_command", map[string]any{"cmd": testshell.CWD(), "login": false}).(map[string]any)
			response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] = progress
			return response
		}
		parentCalls.Add(1)
		if strings.Contains(raw, "failed Run") {
			select {
			case parentFailure <- observed{progressCount: strings.Count(raw, progress), errorCount: strings.Count(raw, "HTTP 401")}:
			default:
			}
		}
		return textReply("Parent reviewed the earlier progress and the subsequent model failure.")
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseFailure) }) })
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		if !bytes.Contains(body, []byte("You are a subagent of Session")) {
			return original.RoundTrip(request)
		}
		if childDispatches.Add(1) == 1 {
			return original.RoundTrip(request)
		}
		// Reaching the second model request requires a completed real tool
		// batch. Hold its HTTP failure while the Host delivers passive progress.
		if childDispatches.Load() == 2 {
			close(secondRequest)
		}
		select {
		case <-releaseFailure:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		payload := encode(map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "invalid_api_key", "message": subagentFailureMarker}})
		return &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(payload)), Request: request}, nil
	})
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "mid-run-provider-failure", "Inspect the actual working directory, then report its result.", false)
	select {
	case <-secondRequest:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	toolSucceeded := false
	for _, entry := range subagentTestEntries(t, f, child.SessionID) {
		if entry.Kind == core.EntryToolResult && entry.Tool != nil && entry.Tool.Invocation.Call.ID == "mid-run-inspect" && entry.Tool.Result != nil {
			toolSucceeded = entry.Tool.Result.Outcome == core.OutcomeSucceeded && !entry.Tool.Result.IsError && len(entry.Tool.Result.Content) != 0
		}
	}
	if !toolSucceeded {
		t.Fatal("second model request was not preceded by a successful actual tool execution")
	}
	eventually(t, f.ctx, func() bool {
		for _, entry := range subagentTestEntries(t, f, parent.id) {
			if entry.Message != nil && entry.Message.ContextKind == subagentUpdateKind && strings.Contains(string(encode(entry.Message.Content)), progress) {
				return true
			}
		}
		return false
	})
	if parentCalls.Load() != 1 || parent.core.Load().Snapshot().Running {
		t.Fatal("the successful intermediate progress woke the parent before failure")
	}
	releaseOnce.Do(func() { close(releaseFailure) })
	failed := subagentFailureWait(t, f, child.SessionID)
	subagentFailureAssertNotification(t, f, parent.id, child.SessionID, failed)
	select {
	case got := <-parentFailure:
		if got.progressCount != 1 || got.errorCount != 1 {
			t.Fatalf("parent failure request lost or duplicated prior progress/error: %+v", got)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	f.idle(t, parent.id)
	if parentCalls.Load() != 2 || childDispatches.Load() != 2 {
		t.Fatalf("mid-run failure unexpectedly reran models: parent=%d child=%d", parentCalls.Load(), childDispatches.Load())
	}
}

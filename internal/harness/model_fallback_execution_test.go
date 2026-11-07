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

func modelFallbackTestResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func modelFallbackTestRequest(t *testing.T, request *http.Request) (map[string]any, string) {
	t.Helper()
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	request.Body = io.NopCloser(bytes.NewReader(raw))
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded, string(raw)
}

func TestModelFallbackContinuesQuotaThenOverflowWithoutSameModelRepair(t *testing.T) {
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if request["model"] != "fixture-third" {
			t.Errorf("unexpected successful request: %v", request["model"])
		}
		return textReply("Finished with the third candidate.")
	})
	second := anotherModel(t, f)
	third := second
	third.Meta, third.ModelID, third.Name = domain.Meta{}, "fixture-third", "Third model"
	declareFixtureModelRecommendations(t, f, &third)
	if err := f.service.Models.SaveProfile(f.ctx, &third); err != nil {
		t.Fatal(err)
	}
	f.agent.ModelPriority = []core.ModelSelection{{ID: f.agent.DefaultModelID}, {ID: second.ID}, {ID: third.ID}}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	var quotaCalls, overflowCalls atomic.Int64
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		decoded, _ := modelFallbackTestRequest(t, request)
		switch decoded["model"] {
		case "fixture":
			quotaCalls.Add(1)
			return modelFallbackTestResponse(request, 402, `{"error":{"message":"quota exhausted","code":"insufficient_quota"}}`), nil
		case "fixture-next":
			overflowCalls.Add(1)
			return modelFallbackTestResponse(request, 400, `{"error":{"message":"maximum context length exceeded","code":"context_length_exceeded"}}`), nil
		default:
			return original.RoundTrip(request)
		}
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "ordered", Text: "Complete this same task."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if quotaCalls.Load() != 1 || overflowCalls.Load() != 1 || f.count() != 1 {
		t.Fatalf("fallback retried a permanent failure or compacted overflow: quota=%d overflow=%d successful=%d", quotaCalls.Load(), overflowCalls.Load(), f.count())
	}
	selection, err := f.service.ModelSelection(f.ctx, f.session.ID)
	if err != nil || selection.ID != third.ID {
		t.Fatalf("did not finish using third model: %+v %v", selection, err)
	}
}

func TestModelFallbackClassifiesCoreReplyValidationFailures(t *testing.T) {
	for _, kind := range []string{"incomplete", "invalid_tool_batch"} {
		t.Run(kind, func(t *testing.T) {
			var firstCalls atomic.Int64
			f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
				if request["model"] != "fixture" {
					return textReply("Recovered after reply validation.")
				}
				firstCalls.Add(1)
				if kind == "invalid_tool_batch" {
					reply := toolReply("duplicate-call", "any_tool", map[string]any{}).(map[string]any)
					message := reply["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
					calls := message["tool_calls"].([]any)
					message["tool_calls"] = append(calls, calls[0])
					return reply
				}
				reply := textReply("truncated").(map[string]any)
				reply["choices"].([]any)[0].(map[string]any)["finish_reason"] = "length"
				return reply
			})
			second := anotherModel(t, f)
			f.agent.ModelPriority = []core.ModelSelection{{ID: f.agent.DefaultModelID}, {ID: second.ID}}
			if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: kind, Text: "Recover this failed model response."}); err != nil {
				t.Fatal(err)
			}
			f.idle(t, f.session.ID)
			if firstCalls.Load() != 1 || f.count() != 2 {
				t.Fatalf("invalid model response was not advanced once: primary=%d all=%d", firstCalls.Load(), f.count())
			}
		})
	}
}

func TestModelFallbackNaturalCompletionCancelsInflightRecoveryProbe(t *testing.T) {
	fallbackStarted, releaseFallback := make(chan struct{}), make(chan struct{})
	probeStarted, probeCancelled := make(chan struct{}), make(chan struct{})
	var releaseOnce, fallbackOnce, probeOnce sync.Once
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if request["model"] != "fixture-next" {
			t.Errorf("unexpected conversation model: %v", request["model"])
		}
		fallbackOnce.Do(func() { close(fallbackStarted) })
		select {
		case <-releaseFallback:
			return textReply("The fallback completed naturally.")
		case <-ctx.Done():
			return nil
		}
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseFallback) }) })
	second := anotherModel(t, f)
	f.agent.ModelPriority = []core.ModelSelection{{ID: f.agent.DefaultModelID}, {ID: second.ID}}
	f.agent.ModelAutoRestore = true
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		decoded, raw := modelFallbackTestRequest(t, request)
		if decoded["model"] != "fixture" {
			return original.RoundTrip(request)
		}
		if strings.Contains(raw, "Check that generation is available.") {
			probeOnce.Do(func() { close(probeStarted) })
			<-request.Context().Done()
			close(probeCancelled)
			return nil, request.Context().Err()
		}
		return modelFallbackTestResponse(request, 402, `{"error":{"message":"quota exhausted","code":"insufficient_quota"}}`), nil
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "probe", Text: "Complete the task while checking recovery."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fallbackStarted:
	case <-f.ctx.Done():
		t.Fatal("fallback never started")
	}
	f.service.modelFallbackMu.Lock()
	var state modelFallbackState
	err := f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state)
	if err == nil {
		state.NextProbeAt = time.Now().Add(-time.Second)
		err = f.service.saveModelFallback(f.ctx, state)
	}
	f.service.modelFallbackMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-probeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("preferred-model probe never started")
	}
	releaseOnce.Do(func() { close(releaseFallback) })
	// Deliberately do not call View/Pending here: the observer must cancel the
	// detector itself while the per-session reconciler is blocked in its SDK call.
	select {
	case <-probeCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("natural task completion left a recovery probe running")
	}
	f.idle(t, f.session.ID)
}

func TestModelFallbackPendingDoesNotFinishNewTaskBeforeItsRunIsObserved(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "old", Text: "Complete an earlier task."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	err = r.prepareModelFallback(f.ctx)
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := f.service.ModelFallbackPending(f.ctx, r.id); err != nil || !pending {
		t.Fatalf("previous task's completed Run finished a newly prepared task: pending=%v err=%v", pending, err)
	}
}

func TestModelFallbackProbeRequestPreservesUsableReasoningBudget(t *testing.T) {
	budget := 1024
	resolved := core.ResolvedModel{Model: core.ModelFunc{Specification: core.ModelSpec{ID: "probe", MaxOutputTokens: 4096}}, Options: core.GenerationOptions{Reasoning: core.ReasoningOptions{BudgetTokens: &budget}, Stop: []string{"old stop"}, ToolChoice: core.ToolChoice{Mode: "required"}}}
	request, err := modelFallbackProbeRequest(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if request.MaxOutputTokens <= budget || request.MaxOutputTokens > 4096 || request.Options.Reasoning.BudgetTokens == nil || *request.Options.Reasoning.BudgetTokens != budget || request.Options.ToolChoice.Mode != "none" || request.Options.Stop != nil || len(request.Tools) != 0 {
		t.Fatalf("recovery detector request cannot use preferred reasoning settings: %+v", request)
	}
}

func modelFallbackTestMakeProbeDue(t *testing.T, f *fixture) {
	t.Helper()
	f.service.modelFallbackMu.Lock()
	defer f.service.modelFallbackMu.Unlock()
	var state modelFallbackState
	if err := f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state); err != nil {
		t.Fatal(err)
	}
	state.NextProbeAt = time.Now().Add(-time.Second)
	if err := f.service.saveModelFallback(f.ctx, state); err != nil {
		t.Fatal(err)
	}
}

func TestModelFallbackWaitingSurvivesRestartAndProbeRecoveryReusesToolResult(t *testing.T) {
	const marker = "ALREADY_COMPLETED_TOOL_RESULT_8274"
	var toolReplies atomic.Int64
	var resumed atomic.Int64
	f := newFixture(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		raw := string(encode(request))
		if strings.Contains(raw, "Check that generation is available.") {
			return textReply("OK")
		}
		if toolReplies.Add(1) == 1 {
			return toolReply("one-durable-tool", "exec_command", map[string]any{"cmd": testshell.Print(marker), "login": false})
		}
		if !strings.Contains(raw, marker) {
			t.Error("recovery lost the already completed tool result")
		}
		resumed.Add(1)
		return textReply("Completed the original task using its durable tool result.")
	})
	second := anotherModel(t, f)
	f.agent.ModelPriority = []core.ModelSelection{{ID: f.agent.DefaultModelID}, {ID: second.ID}}
	f.agent.ModelAutoRestore = true
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	var primaryRequests, probeRequests atomic.Int64
	var recovered atomic.Bool
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		decoded, raw := modelFallbackTestRequest(t, request)
		if decoded["model"] == "fixture" {
			if strings.Contains(raw, "Check that generation is available.") {
				if probeRequests.Add(1) >= 2 {
					recovered.Store(true)
					return original.RoundTrip(request)
				}
			} else if primaryRequests.Add(1) == 1 || recovered.Load() {
				return original.RoundTrip(request)
			}
		}
		return modelFallbackTestResponse(request, 402, `{"error":{"message":"quota exhausted","code":"insufficient_quota"}}`), nil
	})
	receipt, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "durable-original", Text: "Finish the original work after the connection recovers."})
	if err != nil {
		t.Fatal(err)
	}
	waiting := func() bool {
		f.service.modelFallbackMu.Lock()
		defer f.service.modelFallbackMu.Unlock()
		var state modelFallbackState
		return f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state) == nil && state.Phase == "waiting" && state.Resumable
	}
	eventually(t, f.ctx, waiting)
	f.restart(t)
	if !waiting() {
		t.Fatal("restart dropped durable recovery intent")
	}
	modelFallbackTestMakeProbeDue(t, f)
	eventually(t, f.ctx, func() bool {
		f.service.modelFallbackMu.Lock()
		defer f.service.modelFallbackMu.Unlock()
		var state modelFallbackState
		_, inFlight := f.service.modelFallbackProbes[f.session.ID]
		return probeRequests.Load() == 1 && !inFlight && f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state) == nil && state.Phase == "waiting" && state.NextProbeAt.After(time.Now()) && state.NextProbeAt.Before(time.Now().Add(modelFallbackProbeDelay+time.Second))
	})
	if resumed.Load() != 0 {
		t.Fatal("a failed real probe resumed conversation generation")
	}
	modelFallbackTestMakeProbeDue(t, f)
	eventually(t, f.ctx, func() bool { return resumed.Load() == 1 })
	f.idle(t, f.session.ID)
	if probeRequests.Load() != 2 || toolReplies.Load() != 2 || primaryRequests.Load() != 3 {
		t.Fatalf("recovery repeated or replaced original work: probes=%d replies=%d primary requests=%d", probeRequests.Load(), toolReplies.Load(), primaryRequests.Load())
	}
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.History().Scan(f.ctx, core.Query{Kind: core.EntryToolStarted, Limit: 100})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Tool.Invocation.Call.ID != "one-durable-tool" {
		t.Fatalf("automatic recovery replayed a completed tool: entries=%+v error=%v", page.Entries, err)
	}
	if _, err := session.LookupInput(f.ctx, receipt.InputID); err != nil {
		t.Fatalf("original input was lost across recovery: %v", err)
	}
}

func TestModelFallbackDoesNotResumeOrdinaryInterruptedTaskAfterRestart(t *testing.T) {
	started := make(chan struct{})
	var requests atomic.Int64
	f := newFixture(t, 0, func(ctx context.Context, _ int, _ map[string]any) any {
		if requests.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil
		}
		return textReply("Explicitly continued the interrupted task.")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "ordinary", Text: "Do not automatically restart interrupted ordinary work."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-f.ctx.Done():
		t.Fatal("ordinary task never started")
	}
	f.restart(t)
	eventually(t, f.ctx, func() bool {
		f.service.modelFallbackMu.Lock()
		defer f.service.modelFallbackMu.Unlock()
		var state modelFallbackState
		return f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state) == nil && !fallbackArmed(state)
	})
	if requests.Load() != 1 {
		t.Fatalf("model recovery unexpectedly restarted ordinary interrupted work: %d requests", requests.Load())
	}
	state, err := f.coreStore.LoadState(f.ctx, f.session.ID)
	if err != nil || state.LastRun == nil || state.LastRun.Status != "cancelled" {
		t.Fatalf("ordinary interrupted Core state was changed: %+v %v", state.LastRun, err)
	}
	if err := f.service.Continue(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if requests.Load() != 2 {
		t.Fatal("explicit Continue did not resume the preserved ordinary task")
	}
}

func TestModelFallbackProbeRejectsChangedModelRevision(t *testing.T) {
	fallbackStarted, probeStarted, releaseProbe := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var fallbackOnce, probeOnce, releaseOnce sync.Once
	f := newFixture(t, 0, func(ctx context.Context, _ int, _ map[string]any) any {
		fallbackOnce.Do(func() { close(fallbackStarted) })
		<-ctx.Done()
		return nil
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseProbe) }) })
	second := anotherModel(t, f)
	f.agent.ModelPriority = []core.ModelSelection{{ID: f.agent.DefaultModelID}, {ID: second.ID}}
	f.agent.ModelAutoRestore = true
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		decoded, raw := modelFallbackTestRequest(t, request)
		if decoded["model"] != "fixture" {
			return original.RoundTrip(request)
		}
		if strings.Contains(raw, "Check that generation is available.") {
			probeOnce.Do(func() { close(probeStarted) })
			select {
			case <-releaseProbe:
				return modelFallbackTestResponse(request, 200, string(encode(textReply("OK")))), nil
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		return modelFallbackTestResponse(request, 402, `{"error":{"message":"quota exhausted","code":"insufficient_quota"}}`), nil
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "revision", Text: "Keep working while model settings change."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fallbackStarted:
	case <-f.ctx.Done():
		t.Fatal("fallback never started")
	}
	modelFallbackTestMakeProbeDue(t, f)
	select {
	case <-probeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery probe never started")
	}
	var first domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &first); err != nil {
		t.Fatal(err)
	}
	if first.Overrides == nil {
		first.Overrides = map[string]json.RawMessage{}
	}
	first.Overrides["context_window"] = encode(127000)
	if err := f.service.Models.SaveProfile(f.ctx, &first); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(releaseProbe) })
	eventually(t, f.ctx, func() bool {
		f.service.modelFallbackMu.Lock()
		defer f.service.modelFallbackMu.Unlock()
		_, inFlight := f.service.modelFallbackProbes[f.session.ID]
		return !inFlight
	})
	selection, err := f.service.ModelSelection(f.ctx, f.session.ID)
	if err != nil || selection.ID != second.ID {
		t.Fatalf("stale successful probe restored a different configuration: %+v %v", selection, err)
	}
	if err := f.service.Stop(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
}

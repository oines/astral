package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
)

func TestNewSessionSkipsDisabledPreferredModelWithoutLosingPreference(t *testing.T) {
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if request["model"] != "fixture-next" {
			t.Errorf("dispatched disabled preferred model: %v", request["model"])
		}
		return textReply("done")
	})
	second := anotherModel(t, f)
	preferred := core.ModelSelection{ID: f.agent.DefaultModelID}
	f.agent.ModelPriority = []core.ModelSelection{preferred, {ID: second.ID}}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	var first domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", preferred.ID, &first); err != nil {
		t.Fatal(err)
	}
	disabled := false
	first.Enabled = &disabled
	if err := f.service.Models.SaveProfile(f.ctx, &first); err != nil {
		t.Fatal(err)
	}
	created, err := f.service.CreateSession(f.ctx, f.agent.ID, "Disabled preferred")
	if err != nil {
		t.Fatal(err)
	}
	selection, err := f.service.ModelSelection(f.ctx, created.ID)
	if err != nil || selection.ID != second.ID {
		t.Fatalf("new Session did not choose usable fallback: %+v %v", selection, err)
	}
	saved, err := f.service.modelPreferredSelection(f.ctx, created.ID, selection)
	if err != nil || saved.ID != preferred.ID {
		t.Fatalf("disabled preferred reference was lost: %+v %v", saved, err)
	}
	if _, err = f.service.Submit(f.ctx, created.ID, InputRequest{ID: "run", Text: "continue"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, created.ID)
	if f.count() != 1 {
		t.Fatal("new Session called a disabled resource or performed an unsolicited check")
	}
}

func TestWaitingFallbackRemainsBusyForIMAndProbeFollowsActiveModel(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("unused") })
	second := anotherModel(t, f)
	f.agent.ModelPriority = []core.ModelSelection{{ID: f.agent.DefaultModelID}, {ID: second.ID}}
	f.agent.ModelAutoRestore = true
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: 402, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"quota exhausted","code":"insufficient_quota"}}`)), Request: request}, nil
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "work", Text: "keep working"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		selection, err := f.service.ModelSelection(f.ctx, f.session.ID)
		if err != nil || selection.ID != second.ID || requests.Load() < 2 {
			return false
		}
		runtime, err := f.service.open(f.ctx, f.session.ID)
		return err == nil && !runtime.core.Load().Snapshot().Running
	})
	view, err := f.service.View(f.ctx, f.session.ID)
	if err != nil || view.Status != "running" || view.Error != "" || view.CanContinue {
		t.Fatalf("waiting task exposed terminal failure: %+v %v", view, err)
	}
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	canProbe, err := f.service.IMCanProbe(f.ctx, f.session.ID, session)
	if err != nil || canProbe {
		t.Fatalf("waiting task became idle for Probe: %v %v", canProbe, err)
	}
	selection, err := f.service.ModelSelection(f.ctx, f.session.ID)
	if err != nil || selection.ID != second.ID {
		t.Fatalf("Probe does not follow active model: %+v %v", selection, err)
	}
	observation := core.User("new message")
	observation.ContextKind = "im.observation"
	observation.Metadata = encode(messages.Metadata{Source: "im", EventID: "while-waiting", ConversationID: "chat"})
	trigger := core.User("wake up")
	receipt, err := f.service.Deliver(f.ctx, f.session.ID, observation, &trigger)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := session.LookupInput(f.ctx, receipt.InputID)
	if err != nil || stored.Kind != core.InputContext {
		t.Fatalf("waiting task got an extra driver: %+v %v", stored, err)
	}
	pause := &pausedIMFixture{}
	pause.paused.Store(true)
	f.service.IM = pause
	pausedView, err := f.service.View(f.ctx, f.session.ID)
	if err != nil || pausedView.Status != "needs_attention" || pausedView.Error == "" {
		t.Fatalf("fallback hid a separate paused IM delivery after a failed Run: %+v %v", pausedView, err)
	}
	pause.paused.Store(false)
	resumedView, err := f.service.View(f.ctx, f.session.ID)
	if err != nil || resumedView.Status != "running" || resumedView.Error != "" {
		t.Fatalf("clearing the IM pause lost the original fallback wait: %+v %v", resumedView, err)
	}
	if err = f.service.Stop(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if pending, err := f.service.ModelFallbackPending(f.ctx, f.session.ID); err != nil || pending {
		t.Fatalf("Stop retained recovery intent: %v %v", pending, err)
	}
	f.restart(t)
	if pending, err := f.service.ModelFallbackPending(f.ctx, f.session.ID); err != nil || pending {
		t.Fatalf("restart revived stopped recovery: %v %v", pending, err)
	}
}

func TestQueuedSubagentUsesBotFallbackAfterItsPreferredModelIsDisabled(t *testing.T) {
	var disabledCalls atomic.Int64
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if request["model"] == "fixture-next" {
			disabledCalls.Add(1)
		}
		return textReply("Completed the delegated work.")
	})
	parent := subagentTestSeed(t, f)
	next := anotherModel(t, f)
	subagentRecoveryStopWorker(f)
	child, err := f.service.spawnSubagent(f.ctx, parent, core.Invocation{SessionID: parent.id, ExecutionID: "queued-fallback"}, SpawnAgentInput{Task: "Work after model configuration changes.", Model: next.ID})
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	next.Enabled = &disabled
	if err = f.service.Models.SaveProfile(f.ctx, &next); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	eventually(t, f.ctx, func() bool {
		state, err := f.coreStore.LoadState(f.ctx, child.SessionID)
		return err == nil && state.LastRun != nil && state.LastRun.Status == "completed"
	})
	selection, err := f.service.ModelSelection(f.ctx, child.SessionID)
	if err != nil || selection.ID != f.agent.DefaultModelID {
		t.Fatalf("queued child did not use Bot fallback: %+v %v", selection, err)
	}
	preferred, err := f.service.modelPreferredSelection(f.ctx, child.SessionID, selection)
	if err != nil || preferred.ID != next.ID {
		t.Fatalf("child lost its independent preferred model: %+v %v", preferred, err)
	}
	if disabledCalls.Load() != 0 {
		t.Fatal("queued child dispatched a disabled model")
	}
}

func TestModelFallbackSkipsCandidateWhoseLocalContextBudgetCannotFit(t *testing.T) {
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if request["model"] != "fixture-third" {
			t.Errorf("unexpected generation target: %v", request["model"])
		}
		return textReply("Finished with enough context space.")
	})
	small := anotherModel(t, f)
	small.Overrides["context_window"] = encode(8192)
	small.Overrides["max_output_tokens"] = encode(1024)
	// Its fixed prompt/tool schema cannot fit, even after ordinary compaction.
	small.Overrides["max_input_tokens"] = encode(512)
	if err := f.service.Models.SaveProfile(f.ctx, &small); err != nil {
		t.Fatal(err)
	}
	large := small
	large.Meta, large.ModelID, large.Name = domain.Meta{}, "fixture-third", "Larger fallback"
	large.Overrides = make(map[string]json.RawMessage)
	large.Overrides["context_window"], large.Overrides["max_output_tokens"] = encode(64000), encode(4096)
	declareFixtureModelRecommendations(t, f, &large)
	if err := f.service.Models.SaveProfile(f.ctx, &large); err != nil {
		t.Fatal(err)
	}
	f.agent.ModelPriority = []core.ModelSelection{{ID: f.agent.DefaultModelID}, {ID: small.ID}, {ID: large.ID}}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	var smallCalls atomic.Int64
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		body, _ := modelFallbackTestRequest(t, request)
		if body["model"] == "fixture" {
			return modelFallbackTestResponse(request, 402, `{"error":{"code":"insufficient_quota","message":"quota exhausted"}}`), nil
		}
		if body["model"] == "fixture-next" {
			smallCalls.Add(1)
		}
		return original.RoundTrip(request)
	})
	text := strings.Repeat("Substantial original task context that must be preserved. ", 2000)
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "local-budget", Text: text}); err != nil {
		t.Fatal(err)
	}
	view := f.idle(t, f.session.ID)
	if view.ProfileID != large.ID || smallCalls.Load() != 0 || f.count() != 1 {
		t.Fatalf("context budget did not advance the candidate: selection=%s smallCalls=%d actualRequests=%d", view.ProfileID, smallCalls.Load(), f.count())
	}
}

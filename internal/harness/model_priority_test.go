package harness

import (
	"context"
	"errors"
	"reflect"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestAgentModelPriorityPreservesLegacyClientsAndUnavailableSlots(t *testing.T) {
	f, original, provider, replacement := agentModelRecoveryFixture(t)
	first := core.ModelSelection{ID: original.ID, Reasoning: f.agent.DefaultReasoning}
	second := core.ModelSelection{ID: replacement.ID}
	f.agent.ModelPriority = []core.ModelSelection{first, second}
	f.agent.ModelAutoRestore = true
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Models.DeleteProvider(f.ctx, provider.ID, provider.Revision); err != nil {
		t.Fatal(err)
	}
	// Reordering keeps the old unavailable reference, rather than forcing the
	// user to repair or discard it before any other configuration change.
	f.agent.ModelPriority = []core.ModelSelection{second, first}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal("reordering an unavailable existing slot failed:", err)
	}
	if f.agent.DefaultModelID != replacement.ID || !reflect.DeepEqual(f.agent.DefaultReasoning, second.Reasoning) {
		t.Fatalf("legacy default did not project the first entry: %+v", f.agent)
	}
	// Legacy callers omit model_priority; changing unrelated fields must keep
	// the complete chain, including unavailable references and recovery policy.
	legacy := f.agent
	legacy.ModelPriority = nil
	legacy.Name = "Legacy client rename"
	if err := f.service.SaveAgent(f.ctx, &legacy); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy.ModelPriority, f.agent.ModelPriority) || !legacy.ModelAutoRestore {
		t.Fatalf("legacy edit lost model policy: %+v", legacy)
	}
	// A legacy default reasoning update replaces the first pair only.
	legacy.DefaultReasoning = &core.ReasoningOptions{}
	if err := f.service.SaveAgent(f.ctx, &legacy); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy.ModelPriority[0].Reasoning, legacy.DefaultReasoning) || !reflect.DeepEqual(legacy.ModelPriority[1], first) {
		t.Fatal("legacy default change discarded trailing entries")
	}
	if f.count() != 0 {
		t.Fatal("saving model policy dispatched a model request")
	}
}

func TestAgentModelPriorityRejectsDuplicatesAndNewUnavailableChoices(t *testing.T) {
	f, original, provider, replacement := agentModelRecoveryFixture(t)
	f.agent.ModelPriority = []core.ModelSelection{{ID: original.ID, Reasoning: f.agent.DefaultReasoning}, {ID: replacement.ID}}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Models.DeleteProvider(f.ctx, provider.ID, provider.Revision); err != nil {
		t.Fatal(err)
	}
	invalidTopP := 2.0
	for _, selections := range [][]core.ModelSelection{
		{{ID: replacement.ID}, {ID: replacement.ID}},
		{{ID: ""}},
		{{ID: original.ID, Reasoning: &core.ReasoningOptions{Effort: "low"}}},
		{{ID: replacement.ID}, {ID: "missing-profile"}},
		{{ID: replacement.ID, Options: core.GenerationOptions{TopP: &invalidTopP}}},
	} {
		candidate := f.agent
		candidate.ModelPriority = selections
		if err := f.service.SaveAgent(f.ctx, &candidate); err == nil {
			t.Fatalf("accepted invalid model priority: %+v", selections)
		}
		var saved domain.Agent
		if err := f.store.Get(f.ctx, "agents", f.agent.ID, &saved); err != nil || !reflect.DeepEqual(saved, f.agent) {
			t.Fatalf("rejected edit changed saved Bot: %+v, %v", saved, err)
		}
	}
	f.agent.ModelPriority = []core.ModelSelection{}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal("could not clear model priority:", err)
	}
	if f.agent.DefaultModelID != "" || f.agent.DefaultReasoning != nil || len(AgentModelPriority(f.agent)) != 0 {
		t.Fatalf("clearing priority retained legacy default: %+v", f.agent)
	}
	if _, err := f.service.ApplyAgentModelPriority(f.ctx, f.agent.ID); err == nil {
		t.Fatal("applied an empty model priority")
	}
}

func TestApplyAgentModelPriorityUpdatesAllSessionsWithoutStartingThem(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("applying model priority started a model request")
		return textReply("unexpected")
	})
	replacement := anotherModel(t, f)
	otherRoot, err := f.service.CreateSession(f.ctx, f.agent.ID, "Another root")
	if err != nil {
		t.Fatal(err)
	}
	otherBot := domain.Agent{Name: "Other Bot", DefaultModelID: f.agent.DefaultModelID}
	if err = f.service.SaveAgent(f.ctx, &otherBot); err != nil {
		t.Fatal(err)
	}
	otherBotSession, err := f.service.CreateSession(f.ctx, otherBot.ID, "Unaffected")
	if err != nil {
		t.Fatal(err)
	}
	// A persisted child can be queued and not yet have a Core Session. The
	// bulk action must also update its saved selection without running it.
	parent, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	subagentRecoveryStopWorker(f)
	child := subagentTestSpawn(t, f, parent, "bulk-queued", "Queued work", false)
	if _, readErr := f.coreStore.LoadState(f.ctx, child.SessionID); !errors.Is(readErr, core.ErrNotFound) {
		t.Fatalf("queued child was unexpectedly initialized: %v", readErr)
	}
	preferred, readErr := f.service.PreferredModelSelection(f.ctx, child.SessionID)
	if readErr != nil || preferred.ID != f.agent.DefaultModelID {
		t.Fatalf("could not inspect a queued child's saved preference: %+v %v", preferred, readErr)
	}
	wanted := core.ModelSelection{ID: replacement.ID}
	f.agent.ModelPriority = []core.ModelSelection{wanted, {ID: f.agent.DefaultModelID}}
	if err = f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	updated, err := f.service.ApplyAgentModelPriority(f.ctx, f.agent.ID)
	if err != nil || updated != 3 {
		t.Fatalf("bulk apply: updated=%d err=%v", updated, err)
	}
	for _, id := range []string{f.session.ID, otherRoot.ID, child.SessionID} {
		selection, readErr := f.service.ModelSelection(f.ctx, id)
		if readErr != nil || !reflect.DeepEqual(selection, wanted) {
			t.Fatalf("Session %s retained old selection: %+v %v", id, selection, readErr)
		}
		view, readErr := f.service.View(f.ctx, id)
		if readErr != nil || view.Status == "running" {
			t.Fatalf("bulk apply activated Session %s: %+v %v", id, view, readErr)
		}
	}
	selection, err := f.service.ModelSelection(f.ctx, otherBotSession.ID)
	if err != nil || selection.ID != otherBot.DefaultModelID {
		t.Fatal("bulk apply changed another Bot")
	}
	var settings subagentSettings
	if err = f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil || !reflect.DeepEqual(settings.Configuration.Selection, wanted) {
		t.Fatalf("queued subagent retained stale configuration: %+v %v", settings.Configuration.Selection, err)
	}
	if updated, err = f.service.ApplyAgentModelPriority(f.ctx, f.agent.ID); err != nil || updated != 3 {
		t.Fatalf("bulk retry: updated=%d err=%v", updated, err)
	}
	if f.count() != 0 {
		t.Fatal("bulk application dispatched a request")
	}
}

func TestApplyAgentModelPriorityRecoversCursorWithoutRevivingStoppedSession(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("recovering a model preference change started inference")
		return textReply("unexpected")
	})
	second, err := f.service.CreateSession(f.ctx, f.agent.ID, "Second root")
	if err != nil {
		t.Fatal(err)
	}
	target := anotherModel(t, f)
	wanted := core.ModelSelection{ID: target.ID}
	if err = f.service.Stop(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	// Simulate the durable boundary after the first Session was updated but
	// before the next Session. Reopening must resume the remaining absolute
	// preference changes while preserving the stopped task's cancellation.
	if err = f.service.SelectModel(f.ctx, f.session.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	change := agentModelPriorityChange{AgentID: f.agent.ID, Selection: wanted, SessionIDs: []string{f.session.ID, second.ID}, Cursor: 1}
	if err = f.store.SaveMaintenance(f.ctx, agentModelPriorityChangeKind, f.agent.ID, "running", change); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	eventually(t, f.ctx, func() bool {
		var pending agentModelPriorityChange
		return domain.IsMissing(f.store.Maintenance(f.ctx, agentModelPriorityChangeKind, f.agent.ID, &pending))
	})
	for _, id := range change.SessionIDs {
		selection, readErr := f.service.ModelSelection(f.ctx, id)
		if readErr != nil || !reflect.DeepEqual(selection, wanted) {
			t.Fatalf("recovered preference for %s: %+v %v", id, selection, readErr)
		}
	}
	var stopped modelFallbackState
	if err = f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &stopped); err != nil || stopped.Phase != "cancelled" {
		t.Fatalf("bulk recovery revived a stopped Session: %+v %v", stopped, err)
	}
	if pending, readErr := f.service.ModelFallbackPending(f.ctx, f.session.ID); readErr != nil || pending {
		t.Fatalf("stopped task became pending: %v %v", pending, readErr)
	}
	if f.count() != 0 {
		t.Fatal("bulk recovery dispatched inference")
	}
}

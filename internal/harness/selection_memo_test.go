package harness

import (
	"context"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestSelectionMemoFollowsConfigurationAndReturnsIndependentValues(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("No generation needed.") })
	profile := subagentTreeModel(t, f)
	f.restart(t)
	subagentRecoveryStopWorker(f)
	counted := &coldQueryStore{Store: f.coreStore}
	f.service.CoreStore = counted
	for range 3 {
		if _, err := f.service.Summary(f.ctx, f.session.ID); err != nil {
			t.Fatal(err)
		}
	}
	if counted.configReads.Load() != 1 || len(f.service.runtimes) != 0 {
		t.Fatalf("unchanged cold configuration was decoded repeatedly: reads=%d runtimes=%d", counted.configReads.Load(), len(f.service.runtimes))
	}
	for _, effort := range []string{"high", "low"} {
		if err := f.service.SelectModel(f.ctx, f.session.ID, profile.ID, &core.ReasoningOptions{Effort: effort}); err != nil {
			t.Fatal(err)
		}
		counted.resetReads()
		for range 3 {
			view, err := f.service.Summary(f.ctx, f.session.ID)
			if err != nil || view.Selection.ID != profile.ID || view.Reasoning == nil || view.Reasoning.Effort != effort {
				t.Fatalf("configuration change did not update the model/reasoning: %+v err=%v", view.Selection, err)
			}
			view.Reasoning.Effort = "mutated-response"
		}
		if counted.configReads.Load() != 1 {
			t.Fatalf("new immutable configuration should be decoded once: %d", counted.configReads.Load())
		}
	}
	if err := f.service.closeDeletedRuntime(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	f.service.removeDeletedRuntime(f.session.ID)
	if _, exists := f.service.selections[f.session.ID]; exists {
		t.Fatal("removed Session retained its model selection memo")
	}
	counted.resetReads()
	if _, err := f.service.Summary(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if counted.configReads.Load() != 1 || len(f.service.runtimes) != 0 {
		t.Fatal("cold reconstruction did not reload exactly its immutable selection")
	}
}

func TestChildMetadataMemoFollowsSettingsRevision(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Finished.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "revision-child", "Finish.", false)
	state := subagentTestFinished(t, f, child.SessionID)
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	f.idle(t, parent.id)
	profile := subagentTreeModel(t, f)
	f.restart(t)
	subagentRecoveryStopWorker(f)
	before, err := f.service.childMetadata(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	wanted := core.ModelSelection{ID: profile.ID, Reasoning: &core.ReasoningOptions{Effort: "high"}}
	if err := f.service.selectDirectSubagentModel(f.ctx, parent.id, child.SessionID, wanted); err != nil {
		t.Fatal(err)
	}
	after, err := f.service.childMetadata(f.ctx, child.SessionID)
	if err != nil || after.revision <= before.revision || after.fingerprint == before.fingerprint || len(f.service.runtimes) != 0 {
		t.Fatalf("settings revision did not invalidate admission metadata: before=%+v after=%+v runtimes=%d err=%v", before, after, len(f.service.runtimes), err)
	}
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	selected, err := f.service.ModelSelection(f.ctx, child.SessionID)
	if err != nil || !jsonEqual(selected, wanted) {
		t.Fatalf("new child model was not actually synchronized: %+v err=%v", selected, err)
	}
}

func largeSelectionFixture(t *testing.T) (*fixture, domain.Session, int, *coldQueryStore) {
	t.Helper()
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Finished.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "large-selection-child", "Complete the synthetic task.", false)
	subagentTestFinished(t, f, child.SessionID)
	// Enlarge only the frozen configuration, not the Bot document read by
	// agentAvailable. This isolates configuration copies from persona lookup.
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil {
		t.Fatal(err)
	}
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	configuration := r.core.Load().Configuration()
	configuration.SystemPrompt += strings.Repeat("Synthetic fixed configuration context. ", 3072)
	saveLegacySubagentPrefix(t, f, &settings, configuration)
	r.mu.Lock()
	err = r.core.Load().Configure(f.ctx, configuration)
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	state := r.core.Load().State()
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	f.idle(t, parent.id)
	f.restart(t)
	subagentRecoveryStopWorker(f)
	counted := &coldQueryStore{Store: f.coreStore}
	f.service.CoreStore = counted
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	configuration, err = f.service.configurationAtState(f.ctx, record.ID, state)
	if err != nil {
		t.Fatal(err)
	}
	size := len(encode(configuration))
	if size < 100<<10 {
		t.Fatalf("configuration fixture is only %d bytes", size)
	}
	counted.resetReads()
	return f, record, size, counted
}

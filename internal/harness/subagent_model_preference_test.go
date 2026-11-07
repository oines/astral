package harness

import (
	"context"
	"reflect"
	"testing"

	core "github.com/oines/astral-core"
)

func subagentFallbackPreferenceFixture(t *testing.T) (*fixture, *runtime, core.ModelSelection, core.ModelSelection, core.ModelSelection) {
	t.Helper()
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("synchronizing subagent model preference dispatched inference")
		return textReply("unexpected")
	})
	subagentRecoveryStopWorker(f)
	parent, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	child := subagentTestSpawn(t, f, parent, "preference-baseline", "Queued task", false)
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	a := core.ModelSelection{ID: f.agent.DefaultModelID}
	b := core.ModelSelection{ID: anotherModel(t, f).ID}
	c := core.ModelSelection{ID: anotherModel(t, f).ID}
	// Initialize the saved settings baseline, then simulate an automatic
	// fallback. The saved child settings still represent the user's A choice.
	if err = r.syncSubagentModel(f.ctx); err != nil {
		t.Fatal(err)
	}
	var state modelFallbackState
	if err = f.store.Maintenance(f.ctx, modelFallbackKind, r.id, &state); err != nil {
		t.Fatal(err)
	}
	state.Preferred, state.Active, state.Phase = a, b, "active"
	state.Tried = []string{a.ID}
	if err = f.service.saveModelFallback(f.ctx, state); err != nil {
		t.Fatal(err)
	}
	if err = r.syncSubagentModel(f.ctx); err != nil {
		t.Fatal(err)
	}
	if actual := r.core.Load().Configuration().Selection; !reflect.DeepEqual(actual, b) {
		t.Fatalf("unchanged saved preference replaced automatic fallback: %+v", actual)
	}
	return f, r, a, b, c
}

func TestSubagentSettingsModelChangeReplacesFallbackPreference(t *testing.T) {
	f, child, _, _, chosen := subagentFallbackPreferenceFixture(t)
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.id, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Configuration.Selection = chosen
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
		t.Fatal(err)
	}
	if err := child.syncSubagentModel(f.ctx); err != nil {
		t.Fatal(err)
	}
	var state modelFallbackState
	if err := f.store.Maintenance(f.ctx, modelFallbackKind, child.id, &state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.Preferred, chosen) || !reflect.DeepEqual(state.Active, chosen) || !reflect.DeepEqual(child.core.Load().Configuration().Selection, chosen) {
		t.Fatalf("saved selection was overridden by stale fallback state: %+v", state)
	}
}

func TestSubagentBulkPreferenceWaitsForSettingsWithoutRevertingOrResetting(t *testing.T) {
	f, child, _, _, chosen := subagentFallbackPreferenceFixture(t)
	if err := f.service.setPreferredModel(f.ctx, child.id, chosen); err != nil {
		t.Fatal(err)
	}
	// This is the real bulk/tree boundary: Host preference C is durable, but
	// the child's saved settings still contain A. A request can reach sync now.
	if err := child.syncSubagentModel(f.ctx); err != nil {
		t.Fatal(err)
	}
	var before modelFallbackState
	if err := f.store.Maintenance(f.ctx, modelFallbackKind, child.id, &before); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Preferred, chosen) || !reflect.DeepEqual(child.core.Load().Configuration().Selection, chosen) {
		t.Fatalf("old saved settings reverted accepted bulk preference: %+v", before)
	}
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.id, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Configuration.Selection = chosen
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
		t.Fatal(err)
	}
	if err := child.syncSubagentModel(f.ctx); err != nil {
		t.Fatal(err)
	}
	var after modelFallbackState
	if err := f.store.Maintenance(f.ctx, modelFallbackKind, child.id, &after); err != nil {
		t.Fatal(err)
	}
	if after.Epoch != before.Epoch || !reflect.DeepEqual(after.Preferred, chosen) || !reflect.DeepEqual(after.Active, chosen) {
		t.Fatalf("acknowledging saved settings reset accepted preference: before=%+v after=%+v", before, after)
	}
}

func TestSubagentRepeatedBulkPreferenceRefreshesUnsyncedSettingsBaseline(t *testing.T) {
	f, child, original, _, chosen := subagentFallbackPreferenceFixture(t)
	if err := f.service.setPreferredModel(f.ctx, child.id, chosen); err != nil {
		t.Fatal(err)
	}
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.id, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Configuration.Selection = chosen
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
		t.Fatal(err)
	}
	if err := child.syncSubagentModel(f.ctx); err != nil {
		t.Fatal(err)
	}
	// A separately saved A has not reached a request boundary yet. Applying
	// C again explicitly must refresh the baseline even though C is already
	// the Host preference, before the bulk writer replaces saved A with C.
	settings.Configuration.Selection = original
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
		t.Fatal(err)
	}
	if err := f.service.setPreferredModel(f.ctx, child.id, chosen); err != nil {
		t.Fatal(err)
	}
	if err := child.syncSubagentModel(f.ctx); err != nil {
		t.Fatal(err)
	}
	var state modelFallbackState
	if err := f.store.Maintenance(f.ctx, modelFallbackKind, child.id, &state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.Preferred, chosen) || !reflect.DeepEqual(child.core.Load().Configuration().Selection, chosen) {
		t.Fatalf("same-value explicit preference lost to an older settings write: %+v", state)
	}
}

package harness

import (
	"context"
	"reflect"
	"testing"

	core "github.com/oines/astral-core"
)

func TestSubagentInheritsParentActiveFallbackAsItsOwnPreference(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("creating or inspecting a queued child dispatched inference")
		return textReply("unexpected")
	})
	backup := subagentTreeModel(t, f)
	subagentRecoveryStopWorker(f)
	parent, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	preferred := core.ModelSelection{ID: f.agent.DefaultModelID}
	active := core.ModelSelection{ID: backup.ID, Reasoning: &core.ReasoningOptions{Effort: "high"}}
	var state modelFallbackState
	if err := f.store.Maintenance(f.ctx, modelFallbackKind, parent.id, &state); err != nil {
		t.Fatal(err)
	}
	state.Preferred, state.Active = preferred, active
	if err := f.service.saveModelFallback(f.ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := parent.core.Load().SelectModel(f.ctx, active); err != nil {
		t.Fatal(err)
	}
	child := subagentTestSpawn(t, f, parent, "inherit-active-fallback", "Use the currently running model.", false)
	if !reflect.DeepEqual(child.Selection, active) {
		t.Fatalf("child inherited the parent's unavailable preference instead of its active model: %+v", child.Selection)
	}
	childRuntime, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := childRuntime.core.Load().Configuration().Selection; !reflect.DeepEqual(got, active) {
		t.Fatalf("child did not start with the inherited active model/reasoning: %+v", got)
	}
	childPreferred, err := f.service.modelPreferredSelection(f.ctx, child.SessionID, core.ModelSelection{})
	if err != nil || !reflect.DeepEqual(childPreferred, active) {
		t.Fatalf("child recovery would target the parent's preference instead of its own inherited model: %+v %v", childPreferred, err)
	}
	parentPreferred, err := f.service.modelPreferredSelection(f.ctx, parent.id, core.ModelSelection{})
	if err != nil || !reflect.DeepEqual(parentPreferred, preferred) {
		t.Fatalf("child creation changed the parent's preference: %+v %v", parentPreferred, err)
	}
	explicit, err := f.service.spawnSubagent(f.ctx, parent, core.Invocation{SessionID: parent.id, ExecutionID: "explicit-during-fallback"}, SpawnAgentInput{Task: "Use an explicitly chosen model.", Model: preferred.ID})
	if err != nil || explicit.Selection.ID != preferred.ID {
		t.Fatalf("explicit model did not override the inherited active model: %+v %v", explicit.Selection, err)
	}
	if f.count() != 0 {
		t.Fatal("inheriting a model performed a probe or started queued work")
	}
}

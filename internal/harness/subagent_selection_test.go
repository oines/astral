package harness

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
)

func subagentAssertSelection(t *testing.T, f *fixture, parent, id string, want core.ModelSelection) {
	t.Helper()
	eventually(t, f.ctx, func() bool {
		got, err := f.service.ModelSelection(f.ctx, id)
		return err == nil && got.ID == want.ID && reflect.DeepEqual(got.Reasoning, want.Reasoning)
	})
	result, err := f.service.controlSubagent(f.ctx, parent, "selection-query", AgentControlInput{AgentID: id})
	if err != nil {
		t.Fatal(err)
	}
	view := subagentPublicView(t, result)
	if view.Name == "" || view.Selection.ID != want.ID || !reflect.DeepEqual(view.Selection.Reasoning, want.Reasoning) {
		t.Fatalf("query did not expose desired selection/name: got %+v want %+v", view, want)
	}
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", id, &settings); err != nil || settings.Configuration.Selection.ID != want.ID || !reflect.DeepEqual(settings.Configuration.Selection.Reasoning, want.Reasoning) {
		t.Fatalf("desired selection was not persisted: %+v %v", settings.Configuration.Selection, err)
	}
}

func TestSubagentSpawnReasoningDistinguishesInheritedProfileAndAPIDefaults(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Reasoning selection result.") })
	root := subagentTestSeed(t, f)
	model := subagentTreeModel(t, f)
	if model.Overrides == nil {
		model.Overrides = make(map[string]json.RawMessage)
	}
	model.Overrides["defaults"] = encode(core.GenerationOptions{Reasoning: core.ReasoningOptions{Effort: "high"}})
	if err := f.service.Models.SaveProfile(f.ctx, &model); err != nil {
		t.Fatal(err)
	}
	parentChoice := core.ModelSelection{ID: model.ID, Reasoning: &core.ReasoningOptions{Effort: "low"}}
	subagentSelectTreeModel(t, f, root.id, parentChoice)
	for _, tc := range []struct {
		name      string
		args      map[string]any
		desired   *core.ReasoningOptions
		effective string
	}{
		{"inherit-parent", map[string]any{}, parentChoice.Reasoning, "low"},
		{"explicit-level", map[string]any{"model": model.ID, "reasoning": map[string]any{"effort": "low"}}, &core.ReasoningOptions{Effort: "low"}, "low"},
		{"profile-default", map[string]any{"model": model.ID}, nil, "high"},
		{"api-default", map[string]any{"model": model.ID, "reasoning": map[string]any{}}, &core.ReasoningOptions{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.args["task"] = "Inspect selected reasoning: " + tc.name
			child, err := f.service.spawnSubagent(f.ctx, root, core.Invocation{SessionID: root.id, ExecutionID: "reasoning-" + tc.name}, subagentSpawnJSON(t, tc.args))
			if err != nil {
				t.Fatal(err)
			}
			subagentTestFinished(t, f, child.SessionID)
			subagentAssertSelection(t, f, root.id, child.SessionID, core.ModelSelection{ID: model.ID, Reasoning: tc.desired})
			if request := subagentTestRequest(t, f, child.SessionID); !strings.HasPrefix(request.ModelID, model.ID+"~") || request.Options.Reasoning.Effort != tc.effective {
				t.Fatalf("wrong effective model/reasoning: %+v", request.Options.Reasoning)
			}
		})
	}
	before, err := f.store.List(f.ctx, "sessions")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"task": "No model", "reasoning": map[string]any{"effort": "low"}},
		{"task": "Unsupported level", "model": model.ID, "reasoning": map[string]any{"effort": "imaginary"}},
		{"task": "Conflicting controls", "model": model.ID, "reasoning": map[string]any{"effort": "low", "budget_tokens": 100}},
	} {
		if _, err := f.service.spawnSubagent(f.ctx, root, core.Invocation{SessionID: root.id, ExecutionID: "invalid-" + digest(args)}, subagentSpawnJSON(t, args)); err == nil {
			t.Fatalf("accepted invalid spawn: %+v", args)
		}
	}
	after, err := f.store.List(f.ctx, "sessions")
	if err != nil || len(after) != len(before) {
		t.Fatalf("invalid reasoning left child records: %d -> %d: %v", len(before), len(after), err)
	}
}

func TestSubagentControlModelChangesOnlyDirectTargetAndDoesNotStartRun(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Direct target result.") })
	root := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, root, "direct-select-child", "Selected child.", false)
	subagentTestFinished(t, f, child.SessionID)
	childRuntime, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	grand := subagentTestLegacySpawn(t, f, childRuntime, "direct-select-grand", "Existing grandchild keeps its choice.", false)
	subagentTestFinished(t, f, grand.SessionID)
	sibling := subagentTestSpawn(t, f, root, "direct-select-sibling", "Sibling keeps its choice.", true)
	subagentTestFinished(t, f, sibling.SessionID)
	for _, id := range []string{root.id, child.SessionID, grand.SessionID, sibling.SessionID} {
		f.idle(t, id)
	}
	before := map[string]core.ModelSelection{}
	for _, id := range []string{root.id, grand.SessionID, sibling.SessionID} {
		before[id] = sessionSelection(t, f, id)
	}
	oldState := subagentTestFinished(t, f, child.SessionID)
	oldRequest := subagentTestRequest(t, f, child.SessionID)
	model := subagentTreeModel(t, f)
	want := core.ModelSelection{ID: model.ID, Reasoning: &core.ReasoningOptions{Effort: "high"}}
	control := subagentControlJSON(t, map[string]any{"agent_id": child.SessionID, "model": model.ID, "reasoning": map[string]any{"effort": "high"}})
	result, err := f.service.controlSubagent(f.ctx, root.id, "select-without-run", control)
	if err != nil {
		t.Fatal(err)
	}
	if got := subagentPublicView(t, result).Selection; got.ID != want.ID || !reflect.DeepEqual(got.Reasoning, want.Reasoning) {
		t.Fatalf("control result has stale desired selection: %+v", got)
	}
	subagentAssertSelection(t, f, root.id, child.SessionID, want)
	newState := subagentTestFinished(t, f, child.SessionID)
	if newState.LastRun.ID != oldState.LastRun.ID || !reflect.DeepEqual(subagentTestRequest(t, f, child.SessionID), oldRequest) {
		t.Fatal("selection-only control started or rewrote a Run")
	}
	for id, old := range before {
		if got := sessionSelection(t, f, id); !reflect.DeepEqual(got, old) {
			t.Fatalf("direct control changed another Session %s: %+v -> %+v", id, old, got)
		}
	}
	// A root cannot select its grandchild directly.
	foreign := subagentControlJSON(t, map[string]any{"agent_id": grand.SessionID, "model": model.ID})
	if _, err := f.service.controlSubagent(f.ctx, root.id, "cross-layer-select", foreign); err == nil {
		t.Fatal("root selected a non-direct grandchild")
	}
	// Invalid combinations must not change selection or durably accept input.
	for index, args := range []map[string]any{
		{"agent_id": child.SessionID, "reasoning": map[string]any{"effort": "low"}},
		{"agent_id": child.SessionID, "model": model.ID, "reasoning": map[string]any{"effort": "imaginary"}, "input": "INVALID_SELECTION_INPUT"},
		{"agent_id": child.SessionID, "model": model.ID, "signal": "stop"},
		{"agent_id": child.SessionID, "model": model.ID, "signal": "delete"},
		{"agent_id": child.SessionID, "input": "Conflict", "signal": "stop"},
	} {
		execution := "invalid-control-" + string(rune('a'+index))
		if _, err := f.service.controlSubagent(f.ctx, root.id, execution, subagentControlJSON(t, args)); err == nil {
			t.Fatalf("accepted invalid control: %+v", args)
		}
		if _, err := f.coreStore.LookupInput(f.ctx, child.SessionID, "subagent_instruction_"+execution); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("invalid control accepted input: %v", err)
		}
		subagentAssertSelection(t, f, root.id, child.SessionID, want)
	}
	f.restart(t)
	subagentAssertSelection(t, f, root.id, child.SessionID, want)
	// Composer deliberately has the broader recursive scope.
	want.Reasoning = &core.ReasoningOptions{Effort: "low"}
	subagentSelectTreeModel(t, f, root.id, want)
	subagentWaitTreeSelection(t, f, []string{root.id, child.SessionID, grand.SessionID, sibling.SessionID}, want)
}

func TestSubagentControlAppliesSelectionBeforeInputAndClearsPreviousOverride(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Selected before input.") })
	root := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, root, "selection-before-input", "Initial assignment.", false)
	old := subagentTestFinished(t, f, child.SessionID)
	model := subagentTreeModel(t, f)
	input := subagentControlJSON(t, map[string]any{"agent_id": child.SessionID, "model": model.ID, "reasoning": map[string]any{"effort": "high"}, "input": "SELECTED_INPUT_MARKER"})
	if _, err := f.service.controlSubagent(f.ctx, root.id, "selected-new-input", input); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		state, err := f.coreStore.LoadState(f.ctx, child.SessionID)
		return err == nil && state.LastRun != nil && state.LastRun.ID != old.LastRun.ID && state.LastRun.Status == "completed"
	})
	request := subagentTestRequest(t, f, child.SessionID)
	if !strings.HasPrefix(request.ModelID, model.ID+"~") || request.Options.Reasoning.Effort != "high" || !strings.Contains(string(encode(request.Messages)), "SELECTED_INPUT_MARKER") {
		t.Fatalf("input started before complete selection took effect: model=%s reasoning=%+v", request.ModelID, request.Options.Reasoning)
	}
	// A later explicit model without reasoning clears high rather than merging it.
	other := anotherModel(t, f)
	if _, err := f.service.controlSubagent(f.ctx, root.id, "clear-selection-override", subagentControlJSON(t, map[string]any{"agent_id": child.SessionID, "model": other.ID})); err != nil {
		t.Fatal(err)
	}
	subagentAssertSelection(t, f, root.id, child.SessionID, core.ModelSelection{ID: other.ID})
}

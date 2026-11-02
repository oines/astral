package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

// Decode through JSON so these tests exercise the public tool/result contract.
func subagentSpawnJSON(t *testing.T, value map[string]any) SpawnAgentInput {
	t.Helper()
	var input SpawnAgentInput
	if err := json.Unmarshal(encode(value), &input); err != nil {
		t.Fatal(err)
	}
	return input
}

func subagentControlJSON(t *testing.T, value map[string]any) AgentControlInput {
	t.Helper()
	var input AgentControlInput
	if err := json.Unmarshal(encode(value), &input); err != nil {
		t.Fatal(err)
	}
	return input
}

func subagentPublicView(t *testing.T, value any) struct {
	Name      string              `json:"name"`
	Title     string              `json:"title"`
	Selection core.ModelSelection `json:"selection"`
} {
	t.Helper()
	var view struct {
		Name      string              `json:"name"`
		Title     string              `json:"title"`
		Selection core.ModelSelection `json:"selection"`
	}
	if err := json.Unmarshal(encode(value), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestSubagentNameIsParentChosenOrStableAutomaticCode(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Name result.") })
	root := subagentTestSeed(t, f)
	for _, name := range []string{"资料核查", ""} {
		execution := "name-" + name
		input := subagentSpawnJSON(t, map[string]any{"name": name, "task": "TASK_TITLE_SENTINEL: investigate a very different subject and do not use this instruction as a name."})
		inv := core.Invocation{SessionID: root.id, ExecutionID: execution}
		child, err := f.service.spawnSubagent(f.ctx, root, inv, input)
		if err != nil {
			t.Fatal(err)
		}
		subagentTestFinished(t, f, child.SessionID)
		view := subagentPublicView(t, child)
		if view.Name == "" || view.Title != view.Name {
			t.Fatalf("name/title compatibility missing: %+v", view)
		}
		if name != "" && view.Name != name {
			t.Fatalf("parent name was ignored: got %q want %q", view.Name, name)
		}
		if name == "" && (utf8.RuneCountInString(view.Name) > 32 || strings.Contains(view.Name, "TASK_TITLE_SENTINEL")) {
			t.Fatalf("automatic name came from task or is not a short code: %q", view.Name)
		}
		if _, err := f.service.controlSubagent(f.ctx, root.id, execution+"-new-task", AgentControlInput{AgentID: child.SessionID, Input: "A completely different follow-up assignment."}); err != nil {
			t.Fatal(err)
		}
		subagentTestFinished(t, f, child.SessionID)
		again, err := f.service.spawnSubagent(f.ctx, root, inv, input)
		if err != nil || again.SessionID != child.SessionID || subagentPublicView(t, again).Name != view.Name {
			t.Fatalf("spawn retry changed child identity/name: %+v %v", again, err)
		}
		queried, err := f.service.controlSubagent(f.ctx, root.id, execution+"-query", AgentControlInput{AgentID: child.SessionID})
		if err != nil || subagentPublicView(t, queried).Name != view.Name {
			t.Fatalf("new task renamed persistent child: %+v %v", queried, err)
		}
		var record domain.Session
		if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil || record.Title != view.Name {
			t.Fatalf("name not persisted: %+v %v", record, err)
		}
		f.restart(t)
		root, err = f.service.open(f.ctx, f.session.ID)
		if err != nil {
			t.Fatal(err)
		}
		queried, err = f.service.controlSubagent(f.ctx, root.id, execution+"-restart-query", AgentControlInput{AgentID: child.SessionID})
		if err != nil || subagentPublicView(t, queried).Name != view.Name {
			t.Fatalf("restart changed child name: %+v %v", queried, err)
		}
	}
}

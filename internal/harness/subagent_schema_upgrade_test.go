package harness

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
)

func TestSubagentSavedDelegationSchemasSurviveToolUpgradeContinueAndCompact(t *testing.T) {
	var queried atomic.Bool
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		raw := string(encode(request))
		if strings.Contains(raw, "You are a subagent of Session") && strings.Contains(raw, "LEGACY_SCHEMA_NEXT_INPUT") && !queried.Swap(true) {
			return toolReply("legacy-agent-query", "agent_control", map[string]any{})
		}
		return textReply("Legacy schema remains usable.")
	})
	root := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, root, "legacy-schema-child", strings.Repeat("Keep this delegated background intact for the later explicit compaction. ", 1500), false)
	subagentTestFinished(t, f, child.SessionID)
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil {
		t.Fatal(err)
	}
	// Model a pre-upgrade child: its persisted declarations lack all new fields.
	legacy := map[string]core.ToolDefinition{}
	for index, definition := range settings.Configuration.Tools {
		if definition.Name != "spawn_agent" && definition.Name != "agent_control" {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(definition.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
		properties := schema["properties"].(map[string]any)
		delete(properties, "reasoning")
		if definition.Name == "spawn_agent" {
			delete(properties, "name")
		} else {
			delete(properties, "model")
		}
		definition.Version, definition.Description, definition.Parameters = "astral-1", "Persisted pre-upgrade "+definition.Name+" declaration.", encode(schema)
		settings.Configuration.Tools[index], legacy[definition.Name] = definition, definition
	}
	if len(legacy) != 2 {
		t.Fatal("fixture lacks both delegation tools")
	}
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
		t.Fatal(err)
	}
	// Reopen first so the registered Host implementation uses these saved
	// declarations; the running pre-upgrade registry cannot accept a rewrite.
	f.restart(t)
	childCore, err := f.service.Core(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := childCore.Configure(f.ctx, settings.Configuration); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	reopened, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := (spawnAgentTool{runtime: reopened}).Definition(); !reflect.DeepEqual(got, legacy["spawn_agent"]) {
		t.Fatal("new Host replaced the child's saved spawn declaration")
	}
	if got := (agentControlTool{runtime: reopened}).Definition(); !reflect.DeepEqual(got, legacy["agent_control"]) {
		t.Fatal("new Host replaced the child's saved control declaration")
	}
	before := subagentTestFinished(t, f, child.SessionID)
	if _, err := f.service.controlSubagent(f.ctx, root.id, "legacy-next", AgentControlInput{AgentID: child.SessionID, Input: "LEGACY_SCHEMA_NEXT_INPUT: query direct agents using the existing tool."}); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		state, err := f.coreStore.LoadState(f.ctx, child.SessionID)
		return err == nil && state.LastRun != nil && state.LastRun.ID != before.LastRun.ID && state.LastRun.Status == "completed"
	})
	if !queried.Load() {
		t.Fatal("fixture did not execute a persisted legacy control tool call")
	}
	if got := subagentTestRequest(t, f, child.SessionID).Tools; !reflect.DeepEqual(got, settings.Configuration.Tools) {
		t.Fatal("continuation changed the saved tool declarations")
	}
	if err := f.service.Compact(f.ctx, child.SessionID, "Keep the delegated background and identity."); err != nil {
		t.Fatal(err)
	}
	if got := reopened.core.Load().Configuration().Tools; !reflect.DeepEqual(got, settings.Configuration.Tools) {
		t.Fatal("compaction changed the saved tool declarations")
	}
	before = subagentTestFinished(t, f, child.SessionID)
	if _, err := f.service.controlSubagent(f.ctx, root.id, "legacy-after-compact", AgentControlInput{AgentID: child.SessionID, Input: "Continue after compaction with the existing delegation schema."}); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		state, err := f.coreStore.LoadState(f.ctx, child.SessionID)
		return err == nil && state.LastRun != nil && state.LastRun.ID != before.LastRun.ID && state.LastRun.Status == "completed"
	})
	request := subagentTestRequest(t, f, child.SessionID)
	if !reflect.DeepEqual(request.Tools, settings.Configuration.Tools) || len(request.Messages) == 0 || request.Messages[0].ContextKind != "summary" {
		t.Fatal("post-compaction continuation lost the old declaration or compacted context")
	}
}

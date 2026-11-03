package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestSubagentCoordinationPromptsKeepRolesChannelsAndSpawnSchema(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Prompt result.") })
	root := subagentTestSeed(t, f)
	for _, scoped := range []bool{false, true} {
		record := f.session
		if scoped {
			record.Meta, record.Title, record.SubagentParentID, record.Source = domain.Meta{ID: "prompt-child"}, "资料核查", root.id, "subagent"
			if err := f.store.Save(f.ctx, "sessions", &record.Meta, &record); err != nil {
				t.Fatal(err)
			}
		}
		r := &runtime{service: f.service, id: record.ID}
		boundary := core.ContextBoundary{SessionID: record.ID}
		identity, err := r.identityContext(f.ctx, boundary)
		if err != nil || len(identity) != 1 || identity[0].Role != core.RoleUser {
			t.Fatalf("missing role identity: %+v %v", identity, err)
		}
		var metadata identityMetadata
		if err := json.Unmarshal(identity[0].Metadata, &metadata); err != nil || metadata.SessionID != record.ID || metadata.ParentID != record.SubagentParentID || metadata.Revision != identityRevision {
			t.Fatalf("identity lost its Host fields: %+v %v", metadata, err)
		}
		text := identity[0].Content[0].Text
		wanted := []string{"Stay responsive to the user", "Don't let ongoing tasks or subagent coordination delay your replies", "Do not ask subagents to poll", "naturally end your Run", "does not abandon the task", "naturally ends or fails"}
		if scoped {
			wanted = []string{`Your agent name is "资料核查"`, "You are a subagent of Session " + root.id, "You do not spawn or manage further subagents", "passive progress context", "Before substantial work", "2–3 tool-use steps", "alongside your next tool call", "Do not wait for your parent's acknowledgment", "Then naturally end the Run without additional tool calls", "bounded checks necessary for your own work are allowed", "Your Session persists", "A natural Run end alone does not imply business success", "send_message is forbidden", "Earlier identities"}
		}
		for _, fragment := range wanted {
			if !strings.Contains(text, fragment) {
				t.Errorf("role=%v lost coordination rule %q", scoped, fragment)
			}
		}
		if strings.Contains(text, "You may manage your own direct subagents") {
			t.Fatal("identity still permits recursive delegation")
		}
		boundary.Context.Request.Messages = append(boundary.Context.Request.Messages, identity...)
		if again, err := r.identityContext(f.ctx, boundary); err != nil || len(again) != 0 {
			t.Fatalf("ordinary collection repeated current role identity: %+v %v", again, err)
		}
		boundary.CompactionID = "next-compaction"
		afterCompact, err := r.identityContext(f.ctx, boundary)
		if err != nil || len(afterCompact) != 1 {
			t.Fatalf("compaction did not restate current identity once: %+v %v", afterCompact, err)
		}
		boundary.Context.Request.Messages = append(boundary.Context.Request.Messages, afterCompact...)
		if again, err := r.identityContext(f.ctx, boundary); err != nil || len(again) != 0 {
			t.Fatalf("same compaction repeated role identity: %+v %v", again, err)
		}
	}
	definition := (spawnAgentTool{runtime: root}).Definition()
	for _, fragment := range []string{"Only user-facing root Sessions may spawn", "independently actionable work", "Do not ask subagents to poll or keep their Runs alive", "when completed or blocked", "end your own Run rather than polling", "name optionally", "inherit_context=true", "model and reasoning", "current Computer", "Returns session_id, name and selection"} {
		if !strings.Contains(definition.Description, fragment) {
			t.Errorf("spawn description lost %q", fragment)
		}
	}
	wantSchema := map[string]any{"type": "object", "properties": map[string]any{
		"name": map[string]any{"type": "string", "maxLength": 100}, "task": map[string]any{"type": "string", "minLength": 1},
		"inherit_context": map[string]any{"type": "boolean"}, "model": map[string]any{"type": "string"},
		"reasoning":   map[string]any{"type": "object", "properties": map[string]any{"enabled": map[string]any{"type": "boolean"}, "effort": map[string]any{"type": "string"}, "budget_tokens": map[string]any{"type": "integer", "minimum": 0}}, "additionalProperties": false},
		"computer_id": map[string]any{"type": "string"}}, "required": []string{"task"}, "additionalProperties": false}
	var gotSchema any
	if err := json.Unmarshal(definition.Parameters, &gotSchema); err != nil || !bytes.Equal(encode(gotSchema), encode(wantSchema)) {
		t.Fatalf("prompt iteration changed spawn schema: %s %v", definition.Parameters, err)
	}
	for _, prompt := range []string{commonSystemPrompt, webSystemPrompt, imSystemPrompt} {
		for _, stale := range []string{"they can delegate further", "including those with their own children", "Manage only direct children."} {
			if strings.Contains(prompt, stale) {
				t.Errorf("shared System still permits recursion: %q", stale)
			}
		}
		if !strings.Contains(prompt, "Only the user-facing root can spawn subagents or use send_message") {
			t.Fatal("shared System lost the root-only permission boundary")
		}
	}
	if !strings.Contains(webSystemPrompt, "replies to the user with assistant text in Web") || !strings.Contains(imSystemPrompt, "assistant text is visible in Web only; use send_message") || !strings.Contains(imSystemPrompt, "IM observations are passive") {
		t.Fatal("coordination prompt changes altered Web/IM response rules")
	}
}

func TestIdentityRevisionUpgradesExistingRootAndChildOnceWithoutRewritingHistory(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		return textReply("The updated role instructions were applied.")
	})
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "identity-v04-child", "Keep this persistent task.", true)
	state := subagentTestFinished(t, f, child.SessionID)
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	f.idle(t, parent.id)
	subagentRecoveryStopWorker(f)
	configuration, err := core.ReadConfiguration(f.ctx, f.coreStore, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// A child retains the exact inherited System/tool prefix saved by the older
	// Host. Only its Host User identity is upgraded at a safe context boundary.
	configuration.SystemPrompt = "Legacy inherited prefix: subagents can delegate further. The latest Host identity User message establishes the current role."
	for index := range configuration.Tools {
		if configuration.Tools[index].Name == "spawn_agent" {
			configuration.Tools[index].Description = "Legacy persistent subagents may delegate further."
		}
	}
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Configuration = configuration
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	legacySequences := map[string]uint64{}
	originals := map[string]map[string][]byte{}
	for _, id := range []string{parent.id, child.SessionID} {
		current, err := f.coreStore.LoadState(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		metadata := map[string]string{"session_id": id}
		text := "Current Astral Session identity: " + id + ". You are the user-facing root agent."
		if id == child.SessionID {
			metadata["parent_session_id"] = parent.id
			text = "Current Astral Session identity: " + id + ". You may manage your own direct subagents."
		}
		message := core.Message{Role: core.RoleUser, ContextKind: identityKind, Metadata: encode(metadata), Content: core.Text(text)}
		entries := []core.Entry{}
		if id == child.SessionID {
			entries = append(entries, core.Entry{Kind: core.EntryConfiguration, Configuration: &configuration})
		}
		entries = append(entries, core.Entry{ID: "legacy-role-" + id, Kind: core.EntryContext, Message: &message})
		head := current.Branch.HeadID
		for index := range entries {
			if entries[index].ID == "" {
				entries[index].ID = domain.ID("entry")
			}
			entries[index].SessionID, entries[index].Version, entries[index].Time = id, current.Version+1, time.Now().UTC()
			entries[index].ParentID, head = head, entries[index].ID
		}
		if _, err := f.coreStore.Commit(f.ctx, id, core.Commit{ID: "legacy-identity-version-" + id, ExpectedVersion: current.Version, Entries: entries}); err != nil {
			t.Fatal(err)
		}
		originals[id] = map[string][]byte{}
		for _, entry := range subagentTestEntries(t, f, id) {
			originals[id][entry.ID] = encode(entry)
			if entry.ID == "legacy-role-"+id {
				legacySequences[id] = entry.Sequence
			}
		}
	}
	f.restart(t)
	if _, err := f.service.Submit(f.ctx, parent.id, InputRequest{ID: "root-after-identity-upgrade", Text: "Respond using the current role instructions."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, parent.id)
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "child-after-identity-upgrade", AgentControlInput{AgentID: child.SessionID, Input: "Continue using the current role instructions."}); err != nil {
		t.Fatal(err)
	}
	subagentRecoveryReceipt(t, f, child.SessionID, "subagent_instruction_child-after-identity-upgrade")
	f.idle(t, child.SessionID)
	f.idle(t, parent.id)
	countUpgrades := func(id string) int {
		t.Helper()
		count := 0
		for _, entry := range subagentTestEntries(t, f, id) {
			if old, ok := originals[id][entry.ID]; ok && !bytes.Equal(old, encode(entry)) {
				t.Fatalf("identity upgrade rewrote immutable history %s", entry.ID)
			}
			if entry.Sequence <= legacySequences[id] || entry.Message == nil || entry.Message.ContextKind != identityKind {
				continue
			}
			var metadata identityMetadata
			if json.Unmarshal(entry.Message.Metadata, &metadata) == nil && metadata.SessionID == id && metadata.Revision == identityRevision {
				count++
			}
		}
		return count
	}
	for _, id := range []string{parent.id, child.SessionID} {
		if count := countUpgrades(id); count != 1 {
			t.Fatalf("%s appended %d upgraded identities, want 1", id, count)
		}
	}
	if request := subagentTestRequest(t, f, child.SessionID); request.SystemPrompt != configuration.SystemPrompt || !reflect.DeepEqual(request.Tools, configuration.Tools) {
		t.Fatal("upgrading a child's User identity replaced its saved System/tool prefix")
	}
	for _, id := range []string{parent.id, child.SessionID} {
		if err := f.service.InitializeSession(f.ctx, id); err != nil {
			t.Fatal(err)
		}
		if count := countUpgrades(id); count != 1 {
			t.Fatalf("environment refresh repeated the upgraded identity in %s: %d", id, count)
		}
	}
	f.restart(t)
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "child-after-upgraded-restart", AgentControlInput{AgentID: child.SessionID, Input: "A later task must reuse this identity revision."}); err != nil {
		t.Fatal(err)
	}
	subagentRecoveryReceipt(t, f, child.SessionID, "subagent_instruction_child-after-upgraded-restart")
	f.idle(t, child.SessionID)
	for _, id := range []string{parent.id, child.SessionID} {
		if count := countUpgrades(id); count != 1 {
			t.Fatalf("restart repeated the upgraded identity in %s: %d", id, count)
		}
	}
}

func TestSpawnCoordinationPromptLeavesInitialTaskUnchanged(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Task preserved.") })
	root := subagentTestSeed(t, f)
	task := "\n  Keep this exact instruction.\n第二行保留空白。  \n"
	child := subagentTestSpawn(t, f, root, "raw-v04-task", task, false)
	subagentTestFinished(t, f, child.SessionID)
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil || settings.Task != task {
		t.Fatalf("prompt iteration changed the saved task: %q %v", settings.Task, err)
	}
	count := 0
	for _, message := range subagentTestRequest(t, f, child.SessionID).Messages {
		if message.ContextKind == "astral.subagent_instruction" {
			count++
			if got := message.Content[0].Text; got != task {
				t.Fatalf("initial task was wrapped or rewritten: %q", got)
			}
		}
	}
	if count != 1 {
		t.Fatalf("initial task appeared %d times, want 1", count)
	}
}

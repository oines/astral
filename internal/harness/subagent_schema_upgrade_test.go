package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

// Model the old persisted shape without restoring it as a production authority.
func saveLegacySubagentPrefix(t *testing.T, f *fixture, settings *subagentSettings, configuration core.Configuration) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(encode(settings), &document); err != nil {
		t.Fatal(err)
	}
	document["configuration"], document["message_tool"] = configuration, DefaultMessageTool()
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, document); err != nil {
		t.Fatal(err)
	}
}

func TestSubagentUsesCurrentSharedToolsAfterOpeningLegacySettings(t *testing.T) {
	var edited atomic.Bool
	f := newFixture(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		body := string(encode(request))
		if strings.Contains(body, "You are a subagent of Session") && strings.Contains(body, "USE_UPGRADED_EDIT") && !edited.Swap(true) {
			return toolReply("upgraded-edit", "edit", map[string]any{"path": "upgrade.txt", "edits": []any{map[string]string{"oldText": "before", "newText": "after"}}})
		}
		return textReply("Completed the delegated work.")
	})
	root := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, root, "old-tool-child", "Keep this delegated task intact.", true)
	subagentTestFinished(t, f, child.SessionID)
	f.idle(t, root.id)
	childCore, err := f.service.Core(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := childCore.Attempts().Scan(f.ctx, core.AttemptQuery{Purpose: "generation", Reverse: true, Limit: 1})
	if err != nil || len(attempts.Attempts) != 1 {
		t.Fatalf("missing existing frozen request: %v", err)
	}
	frozen, err := childCore.Attempts().Request(f.ctx, attempts.Attempts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy := childCore.Configuration()
	legacy.SystemPrompt = "Legacy saved System. " + strings.Repeat("Old shared instructions. ", 5000)
	for i := range legacy.Tools {
		switch legacy.Tools[i].Name {
		case "read", "write", "edit":
			legacy.Tools[i].Version, legacy.Tools[i].Description = "astral-2/2", "Old standard tool declaration."
		case "spawn_agent", "agent_control":
			legacy.Tools[i].Version, legacy.Tools[i].Description = "astral-1", "Old delegation tool declaration."
		}
	}
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil {
		t.Fatal(err)
	}
	saveLegacySubagentPrefix(t, f, &settings, legacy)
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	state, err := f.coreStore.LoadState(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	old := core.Entry{ID: domain.ID("old-config"), Kind: core.EntryConfiguration, SessionID: child.SessionID, ParentID: state.Branch.HeadID, Version: state.Version + 1, Time: time.Now().UTC(), Configuration: &legacy}
	if _, err := f.coreStore.Commit(f.ctx, child.SessionID, core.Commit{ID: "old-config", ExpectedVersion: state.Version, Entries: []core.Entry{old}}); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	reopened, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.prefixRootID != root.id {
		t.Fatal("child did not resolve its root prefix source")
	}
	version, runID := reopened.core.Load().State().Version, reopened.core.Load().State().LastRun.ID
	if reopened.core.Load().Configuration().SystemPrompt != legacy.SystemPrompt {
		t.Fatal("read-only open replaced old configuration")
	}
	if _, err := reopened.accept(f.ctx, core.Input{ID: "passive-upgrade", Contexts: []core.Message{core.User("New passive context after the application update.")}}, core.InputContext); err != nil {
		t.Fatal("old declarations blocked passive context admission:", err)
	}
	if snapshot, err := reopened.snapshot(); err != nil || snapshot.Running || snapshot.State.Version <= version || snapshot.State.LastRun.ID != runID {
		t.Fatal("passive prefix synchronization failed or woke a model")
	}
	binding, err := reopened.currentBinding(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(binding.CWD, "upgrade.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.controlSubagent(f.ctx, root.id, "next-upgraded", AgentControlInput{AgentID: child.SessionID, Input: "USE_UPGRADED_EDIT: edit the file now."}); err != nil {
		t.Fatal(err)
	}
	subagentRecoveryReceipt(t, f, child.SessionID, "subagent_instruction_next-upgraded")
	subagentTestFinished(t, f, child.SessionID)
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "after\n" || !edited.Load() {
		t.Fatalf("current standard edit did not execute: %q %v", raw, err)
	}
	request := subagentTestRequest(t, f, child.SessionID)
	for _, definition := range request.Tools {
		if definition.Name == "read" || definition.Name == "write" || definition.Name == "edit" {
			if definition.Version != "astral-2/3" {
				t.Fatalf("new request kept a saved standard declaration: %+v", definition)
			}
		}
	}
	if strings.Contains(request.SystemPrompt, "Legacy saved System") || !strings.HasPrefix(request.SystemPrompt, commonSystemPrompt) {
		t.Fatal("new request kept the saved child System")
	}
	after, err := reopened.core.Load().Attempts().Request(f.ctx, attempts.Attempts[0].ID)
	if err != nil || !reflect.DeepEqual(after, frozen) {
		t.Fatal("updating shared sources rewrote an existing frozen request")
	}
	entry, err := reopened.core.Load().History().Get(f.ctx, old.ID)
	if err != nil || !reflect.DeepEqual(entry.Configuration, &legacy) {
		t.Fatal("updating shared sources rewrote old configuration history")
	}
	before := reopened.core.Load().State().Version
	if err := reopened.syncBusinessConfig(f.ctx); err != nil || reopened.core.Load().State().Version != before {
		t.Fatal("unchanged shared configuration caused another write")
	}
}

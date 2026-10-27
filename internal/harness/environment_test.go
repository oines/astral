package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	testshell "github.com/oines/astral/internal/testkit/shell"
)

func environmentEntries(t *testing.T, f *fixture, id string) []core.Entry {
	t.Helper()
	session, err := f.service.Core(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.History().Scan(f.ctx, core.Query{Kind: core.EntryContext, Limit: 1000, MaxBytes: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var entries []core.Entry
	for _, entry := range page.Entries {
		if entry.Message.ContextKind == environmentKind {
			entries = append(entries, entry)
		}
	}
	return entries
}

func writeEnvironmentFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

// Shells can expand a Windows 8.3 temp path to its long spelling. Verify the
// directory in the actual tool output instead of comparing serialized slashes
// or relying on the spelling retained in an assistant's command arguments.
func toolOutputHasDirectory(request map[string]any, directory string) bool {
	want, err := os.Stat(directory)
	if err != nil {
		return false
	}
	messages, _ := request["messages"].([]any)
	for _, value := range messages {
		message, _ := value.(map[string]any)
		if message["role"] != "tool" {
			continue
		}
		content, _ := message["content"].(string)
		for _, line := range strings.Split(content, "\n") {
			got, err := os.Stat(strings.TrimSpace(line))
			if err == nil && os.SameFile(want, got) {
				return true
			}
		}
	}
	return false
}

func wireEnvironments(request map[string]any) []string {
	var snapshots []string
	for _, raw := range request["messages"].([]any) {
		message := raw.(map[string]any)
		content, _ := message["content"].(string)
		if parts, ok := message["content"].([]any); ok {
			for _, part := range parts {
				if text, ok := part.(map[string]any)["text"].(string); ok {
					content += text
				}
			}
		}
		if strings.HasPrefix(content, "Current Astral environment snapshot:\n") {
			snapshots = append(snapshots, content)
		}
	}
	return snapshots
}

func TestEnvironmentModelViewPreservesAPITargetsWithoutInternalFields(t *testing.T) {
	f := newFixture(t, 2, func(context.Context, int, map[string]any) any { return textReply("done") })
	initial := environmentEntries(t, f, f.session.ID)[0].Message.Content[0].Text
	if !strings.Contains(initial, `"status":"missing"`) || !strings.Contains(initial, `"skill_roots"`) {
		t.Fatal("missing discovery paths became indistinguishable from a successful scan")
	}
	skillPath := filepath.Join(f.daemons[0].Hello.Home, ".agents", "skills", "example", "SKILL.md")
	writeEnvironmentFile(t, skillPath, "---\nname: example\ndescription: An example skill\n---\nSkill body")
	writeEnvironmentFile(t, filepath.Join(f.session.CWD, "AGENTS.md"), "Workspace instructions")
	if err := os.MkdirAll(filepath.Join(f.session.CWD, ".agents", "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	// Populate routing/configuration fields to verify they are treated differently
	// from the private identity of the selected search provider.
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", f.session.ID, &record); err != nil {
		t.Fatal(err)
	}
	record.ConversationID = "context-conversation"
	if err := f.store.Save(f.ctx, "sessions", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	f.agent.SearchProviderID = "private-search-provider"
	if err := f.store.Save(f.ctx, "agents", &f.agent.Meta, &f.agent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "projection", Text: "inspect the environment"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	f.mu.Lock()
	wire := append([]byte(nil), f.requests[0]...)
	f.mu.Unlock()
	var request map[string]any
	if err := json.Unmarshal(wire, &request); err != nil {
		t.Fatal(err)
	}
	snapshots := wireEnvironments(request)
	body := snapshots[len(snapshots)-1]
	for _, hidden := range []string{`"sha256"`, `"boot_id"`, `"search_provider_id"`, "private-search-provider", `"fingerprint"`, `"compaction_id"`} {
		if strings.Contains(body, hidden) {
			t.Errorf("model environment exposes %s", hidden)
		}
	}
	var visible map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(body, "Current Astral environment snapshot:\n")), &visible); err != nil {
		t.Fatal(err)
	}
	agent, session := visible["agent"].(map[string]any), visible["session"].(map[string]any)
	computer := visible["current_computer"].(map[string]any)
	if agent["id"] != f.agent.ID || session["id"] != f.session.ID || session["source"] != record.Source || session["conversation_id"] != record.ConversationID || computer["id"] != record.ComputerID || computer["cwd"] != record.CWD {
		t.Fatalf("model lost its API targets or working directory: %s", body)
	}
	for _, field := range []string{"name", "os", "arch", "shell", "agent_home", "online", "revoked"} {
		if _, exists := computer[field]; !exists {
			t.Errorf("current Computer lost %s", field)
		}
	}
	allowed := visible["allowed_computers"].([]any)
	if len(allowed) != 2 {
		t.Fatal("allowed Computer targets were removed")
	}
	for _, raw := range allowed {
		value := raw.(map[string]any)
		if value["id"] == "" || value["kind"] != nil || value["arch"] != nil {
			t.Fatalf("invalid selectable Computer summary: %+v", value)
		}
	}
	discovery := visible["discovery"].(map[string]any)
	if discovery["computer_id"] != nil || discovery["cwd"] != nil || discovery["skill_roots"] != nil || discovery["status"] != "ok" {
		t.Fatalf("discovery retained repeated identity or successful root diagnostics: %+v", discovery)
	}
	skill := discovery["skills"].([]any)[0].(map[string]any)
	if skill["path"] != skillPath || skill["name"] != "example" || skill["description"] != "An example skill" || skill["scope"] != "global" || discovery["agents_md"].(map[string]any)["content"] != "Workspace instructions" {
		t.Fatal("projection lost usable skills or workspace guidance")
	}
	api := visible["local_api"].(map[string]any)
	if api["base_url"] == "" || api["capabilities_url"] != api["base_url"].(string)+"/v1/capabilities" || visible["web_search"].(map[string]any)["configured"] != true || visible["current_model"].(map[string]any)["id"] != "fixture" {
		t.Fatal("projection lost API discovery, search status or model information")
	}
	entries := environmentEntries(t, f, f.session.ID)
	var metadata environmentMetadata
	if err := json.Unmarshal(entries[len(entries)-1].Message.Metadata, &metadata); err != nil || metadata.SkillHashes[skillPath] == "" || metadata.BootID == "" {
		t.Fatalf("internal change detection lost its hashes or boot identity: %+v, %v", metadata, err)
	}
	if strings.Contains(string(wire), metadata.BootID) || strings.Contains(string(wire), metadata.SkillHashes[skillPath]) {
		t.Fatal("private environment metadata leaked into the actual provider request")
	}
	invalidSkill := filepath.Join(f.session.CWD, ".agents", "skills", "invalid", "SKILL.md")
	writeEnvironmentFile(t, invalidSkill, "Invalid frontmatter")
	if err := f.service.InitializeSession(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	entries = environmentEntries(t, f, f.session.ID)
	partial := entries[len(entries)-1].Message.Content[0].Text
	var partialView struct {
		Discovery struct {
			Status string
			Skills []struct{ Path string }
			Issues []struct{ Path, Message string }
		}
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(partial, environmentSnapshotHeading+"\n")), &partialView); err != nil {
		t.Fatal(err)
	}
	foundIssue, foundSkill := false, false
	for _, issue := range partialView.Discovery.Issues {
		if issue.Path == invalidSkill && strings.Contains(issue.Message, "YAML frontmatter") {
			foundIssue = true
		}
	}
	for _, skill := range partialView.Discovery.Skills {
		if skill.Path == skillPath {
			foundSkill = true
		}
	}
	if partialView.Discovery.Status != "partial" || !foundIssue || !foundSkill {
		t.Fatalf("partial discovery lost its error, affected path or valid skills: %+v", partialView.Discovery)
	}
}

func TestEnvironmentInitializationDedupAndReadsAcrossRestart(t *testing.T) {
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any { return textReply("done") })
	initial := environmentEntries(t, f, f.session.ID)
	if len(initial) != 1 || initial[0].Message.Role != core.RoleUser || f.count() != 0 {
		t.Fatalf("initialization did not append exactly one passive snapshot: %+v", initial)
	}
	binding, err := f.hub.Bind(f.ctx, f.agent.ID, f.session.ID, f.session.ComputerID, f.session.CWD)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(initial[0].Message.Content[0].Text, `"base_url":"`+binding.APIURL+`"`) || !strings.Contains(initial[0].Message.Content[0].Text, binding.APIURL+"/v1/capabilities") || strings.Contains(initial[0].Message.Content[0].Text, "openapi_url") {
		t.Fatal("environment did not expose the single bound API discovery entry")
	}
	if f.session.CWD == "" {
		t.Fatal("resolved default CWD was not persisted")
	}
	other, err := f.service.CreateSession(f.ctx, f.agent.ID, "same Agent")
	if err != nil || other.CWD != f.session.CWD {
		t.Fatalf("new Sessions do not share AgentHome: %q %q, %v", other.CWD, f.session.CWD, err)
	}
	writeEnvironmentFile(t, filepath.Join(f.session.CWD, "AGENTS.md"), "new current guidance")
	globalSkill := filepath.Join(f.daemons[0].Hello.Home, ".agents", "skills", "global", "SKILL.md")
	writeEnvironmentFile(t, globalSkill, "---\nname: global\ndescription: Global test skill\n---\nbody one")
	f.restart(t)
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := encode(session.State())
	if _, err = f.service.View(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	view, err := f.service.InspectContext(f.ctx, f.session.ID)
	if err != nil || string(before) != string(encode(session.State())) || len(environmentEntries(t, f, f.session.ID)) != 1 {
		t.Fatalf("read/reopen mutated history: %v", err)
	}
	if strings.Contains(string(encode(view.Request)), "new current guidance") {
		t.Fatal("read-only projection scanned and injected changed files")
	}
	if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "first", Text: "work"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	entries := environmentEntries(t, f, f.session.ID)
	if len(entries) != 2 || !strings.Contains(entries[1].Message.Content[0].Text, "new current guidance") {
		t.Fatal("execution did not append the changed environment")
	}
	if string(encode(entries[0])) != string(encode(initial[0])) {
		t.Fatal("refresh rewrote earlier context")
	}
	f.restart(t)
	if err = f.service.InitializeSession(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if len(environmentEntries(t, f, f.session.ID)) != 2 {
		t.Fatal("reopening lost durable snapshot deduplication")
	}
	writeEnvironmentFile(t, globalSkill, "---\nname: global\ndescription: Global test skill\n---\nbody two")
	if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "second", Text: "again"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if len(environmentEntries(t, f, f.session.ID)) != 3 || f.count() != 2 {
		t.Fatal("skill body changes were missed or passive context activated an extra Run")
	}
	entries = environmentEntries(t, f, f.session.ID)
	changed := entries[len(entries)-1].Message.Content[0].Text
	var changedView struct{ Updates []string }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(changed, environmentSnapshotHeading+"\n")), &changedView); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(changedView.Updates, "Skill content changed; read this SKILL.md again before using it: "+globalSkill) || strings.Contains(changed, `"sha256"`) {
		t.Fatal("body-only Skill change did not produce a useful notice without exposing a hash")
	}
	f.restart(t)
	if err := f.service.InitializeSession(f.ctx, f.session.ID); err != nil || len(environmentEntries(t, f, f.session.ID)) != 3 {
		t.Fatalf("hidden hashes did not preserve durable deduplication after a Skill change: %v", err)
	}
	state, _ := f.service.View(f.ctx, f.session.ID)
	child, err := f.service.Fork(f.ctx, f.session.ID, state.HeadID, "fork", domain.ID("session"))
	if err != nil || child.CWD != f.session.CWD {
		t.Fatalf("fork lost the selected CWD: %+v, %v", child, err)
	}
	childContext, err := f.service.ReadContext(f.ctx, child.ID)
	if err != nil || childContext.HeadID != state.HeadID || len(environmentEntries(t, f, child.ID)) != 3 {
		t.Fatalf("creating or reading fork appended environment before execution: %v", err)
	}
	if _, err = f.service.Submit(f.ctx, child.ID, InputRequest{ID: "first-child", Text: "continue cloned work"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, child.ID)
	childEnvironments := environmentEntries(t, f, child.ID)
	if len(childEnvironments) != 4 || !strings.Contains(childEnvironments[len(childEnvironments)-1].Message.Content[0].Text, child.ID) {
		t.Fatal("first fork execution did not append its own full environment")
	}
}

func TestEnvironmentRefreshFollowsCompleteToolBatchInSameRun(t *testing.T) {
	var firstMessages []any
	f := newFixture(t, 1, func(_ context.Context, n int, request map[string]any) any {
		messages := request["messages"].([]any)
		if n == 1 {
			firstMessages = messages
			return map[string]any{"id": "batch", "choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "write", "type": "function", "function": map[string]any{"name": "exec_command", "arguments": string(encode(map[string]any{"cmd": testshell.WriteFile("AGENTS.md", "changed by tool"), "login": false}))}},
				map[string]any{"id": "read", "type": "function", "function": map[string]any{"name": "exec_command", "arguments": string(encode(map[string]any{"cmd": testshell.CWD(), "login": false}))}},
			}}}}}
		}
		if n != 2 {
			t.Errorf("passive snapshot caused an extra request: %d", n)
		}
		if !reflect.DeepEqual(firstMessages, messages[:len(firstMessages)]) {
			t.Error("environment refresh changed the previous request prefix")
		}
		snapshots := wireEnvironments(request)
		if len(snapshots) != 2 || !strings.Contains(snapshots[1], "changed by tool") {
			t.Errorf("next request missed the freshly scanned environment: %v", snapshots)
		}
		last := len(messages) - 1
		if messages[last-1].(map[string]any)["role"] != "tool" || messages[last-2].(map[string]any)["role"] != "tool" || messages[last].(map[string]any)["role"] != "user" {
			t.Error("snapshot split the assistant/tool-result batch")
		}
		return textReply("done")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "batch", Text: "update the workspace"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if f.count() != 2 || len(environmentEntries(t, f, f.session.ID)) != 2 {
		t.Fatal("same-Run boundary refresh did not append exactly once")
	}
}

func TestEnvironmentCWDChangesReturnToPriorStateAndCommandWorkdirIsTemporary(t *testing.T) {
	a, b, temporary := t.TempDir(), t.TempDir(), t.TempDir()
	writeEnvironmentFile(t, filepath.Join(a, "AGENTS.md"), "workspace A")
	writeEnvironmentFile(t, filepath.Join(b, "AGENTS.md"), "workspace B")
	var f *fixture
	f = newFixture(t, 1, func(_ context.Context, n int, request map[string]any) any {
		snapshots := wireEnvironments(request)
		last := snapshots[len(snapshots)-1]
		switch n {
		case 1:
			return toolReply("to-b", "switch_computer", map[string]any{"computer_id": f.session.ComputerID, "cwd": b})
		case 2:
			if !strings.Contains(last, "workspace B") || strings.Contains(last, "workspace A") {
				t.Error("switch kept old workspace guidance current")
			}
			return toolReply("to-a", "switch_computer", map[string]any{"computer_id": f.session.ComputerID, "cwd": a})
		case 3:
			if !strings.Contains(last, "workspace A") || strings.Contains(last, "workspace B") {
				t.Error("A -> B -> A incorrectly deduplicated the returning snapshot")
			}
			return toolReply("temporary", "exec_command", map[string]any{"cmd": testshell.CWD(), "workdir": temporary, "login": false})
		case 4:
			if !strings.Contains(last, "workspace A") || !toolOutputHasDirectory(request, temporary) {
				t.Error("temporary workdir changed the Session CWD or failed to reach the tool")
			}
			return textReply("done")
		default:
			t.Errorf("unexpected request %d", n)
			return textReply("unexpected")
		}
	})
	if err := f.service.SelectComputer(f.ctx, f.session.ID, f.session.ComputerID, a); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "switch", Text: "switch workspaces"}); err != nil {
		t.Fatal(err)
	}
	view := f.idle(t, f.session.ID)
	entries := environmentEntries(t, f, f.session.ID)
	if view.CWD != a || len(entries) != 4 || f.count() != 4 {
		t.Fatalf("persistent CWD/snapshot count: cwd=%q snapshots=%d requests=%d", view.CWD, len(entries), f.count())
	}
	var previous, returning environmentMetadata
	_ = json.Unmarshal(entries[1].Message.Metadata, &previous)
	_ = json.Unmarshal(entries[3].Message.Metadata, &returning)
	if previous.Fingerprint != returning.Fingerprint {
		t.Fatal("returning environment is not deterministic")
	}
	other, err := f.service.CreateSession(f.ctx, f.agent.ID, "independent CWD")
	if err != nil || other.CWD == a || other.CWD != f.session.CWD {
		t.Fatalf("one Session's switch changed another Session's default: %+v, %v", other, err)
	}
}

func TestEnvironmentOfflineStatusDoesNotKeepStaleDiscovery(t *testing.T) {
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any { return textReply("offline chat") })
	writeEnvironmentFile(t, filepath.Join(f.session.CWD, "AGENTS.md"), "online guidance")
	if err := f.service.InitializeSession(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.daemons[0].Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		var computer domain.Computer
		return f.store.Get(f.ctx, "computers", f.session.ComputerID, &computer) == nil && !f.hub.Status(computer).Online
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "offline", Text: "chat"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	entries := environmentEntries(t, f, f.session.ID)
	text := entries[len(entries)-1].Message.Content[0].Text
	var snapshot struct {
		Discovery computers.EnvironmentDiscovery `json:"discovery"`
		LocalAPI  map[string]string              `json:"local_api"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(text, "Current Astral environment snapshot:\n")), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.LocalAPI != nil || snapshot.Discovery.Status != "unavailable" || snapshot.Discovery.AgentsMD.Content != "" || f.count() != 1 {
		t.Fatalf("offline state retained stale guidance or prevented chat: %+v", snapshot)
	}
}

func TestEnvironmentAfterCompactScansAgainAndAppendsFullSnapshot(t *testing.T) {
	var f *fixture
	f = newFixture(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		if _, hasTools := request["tools"]; !hasTools {
			writeEnvironmentFile(t, filepath.Join(f.session.CWD, "AGENTS.md"), "changed during compaction")
			return textReply("Short summary of the completed work.")
		}
		return textReply("done")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "long", Text: strings.Repeat("historical conversation detail ", 3500)}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	before := environmentEntries(t, f, f.session.ID)
	if err := f.service.Compact(f.ctx, f.session.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	view, err := f.service.InspectContext(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := view.Request.Messages[len(view.Request.Messages)-1]
	if view.Request.Messages[0].ContextKind != "summary" || last.ContextKind != environmentKind || !strings.Contains(last.Content[0].Text, "changed during compaction") {
		t.Fatal("post-compaction request did not end with a fresh full environment")
	}
	var metadata environmentMetadata
	_ = json.Unmarshal(last.Metadata, &metadata)
	if metadata.CompactionID == "" || len(environmentEntries(t, f, f.session.ID)) != len(before)+1 || f.count() != 2 {
		t.Fatal("post-compaction refresh was not passive and tied to its durable checkpoint")
	}
	f.restart(t)
	if err := f.service.InitializeSession(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if len(environmentEntries(t, f, f.session.ID)) != len(before)+1 {
		t.Fatal("reopen duplicated the post-compaction snapshot")
	}
}

type environmentApplyFault struct {
	core.Store
	fail atomic.Bool
}

func (f *environmentApplyFault) Commit(ctx context.Context, id string, commit core.Commit) (core.Receipt, error) {
	for _, entry := range commit.Entries {
		if entry.Kind == core.EntryContext && entry.Message.ContextKind == environmentKind && f.fail.Swap(false) {
			return core.Receipt{}, errors.New("environment apply failed")
		}
	}
	return f.Store.Commit(ctx, id, commit)
}

func TestEnvironmentCompactionAndSnapshotCommitAtomicallyAfterRestart(t *testing.T) {
	f := newFixture(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		if _, hasTools := request["tools"]; !hasTools {
			return textReply("A short summary.")
		}
		return textReply("done")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "before", Text: strings.Repeat("historical conversation detail ", 3500)}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	before := len(environmentEntries(t, f, f.session.ID))
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	fault := &environmentApplyFault{Store: f.coreStore}
	fault.fail.Store(true)
	f.service = New(f.store, fault, f.service.Models, f.hub)
	if err := f.service.Compact(f.ctx, f.session.ID, ""); err == nil || !strings.Contains(err.Error(), "environment apply failed") {
		t.Fatalf("expected atomic context commit failure: %v", err)
	}
	session, _ := f.service.Core(f.ctx, f.session.ID)
	if session.State().Branch.CompactionID != "" || len(environmentEntries(t, f, f.session.ID)) != before || f.count() != 2 {
		t.Fatal("failed boundary published a summary or partial environment")
	}
	f.restart(t)
	if err := f.service.Compact(f.ctx, f.session.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "resume", Text: "continue"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	session, _ = f.service.Core(f.ctx, f.session.ID)
	entries := environmentEntries(t, f, f.session.ID)
	var metadata environmentMetadata
	_ = json.Unmarshal(entries[len(entries)-1].Message.Metadata, &metadata)
	if len(entries) != before+1 || metadata.CompactionID == "" || session.State().Branch.CompactionID != metadata.CompactionID || f.count() != 4 {
		t.Fatal("restart did not rebuild and commit a single summary/environment boundary")
	}
}

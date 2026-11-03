package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
	testshell "github.com/oines/astral/internal/testkit/shell"
)

func subagentTestSeed(t *testing.T, f *fixture) *runtime {
	t.Helper()
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "root-seed", Text: "Parent context kept exactly as written."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func subagentTestSpawn(t *testing.T, f *fixture, parent *runtime, execution, task string, inherited bool) SubagentView {
	t.Helper()
	child, err := f.service.spawnSubagent(f.ctx, parent, core.Invocation{SessionID: parent.id, ExecutionID: execution}, SpawnAgentInput{Task: task, InheritContext: inherited})
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func subagentTestFinished(t *testing.T, f *fixture, id string) core.RuntimeState {
	t.Helper()
	var state core.RuntimeState
	eventually(t, f.ctx, func() bool {
		var err error
		state, err = f.coreStore.LoadState(f.ctx, id)
		return err == nil && state.LastRun != nil && state.LastRun.Status == "completed" && state.CurrentAttemptID == "" && state.PendingInputs[core.InputSteer]+state.PendingInputs[core.InputFollowUp] == 0
	})
	return state
}

func subagentTestEntries(t *testing.T, f *fixture, id string) []core.Entry {
	t.Helper()
	state, err := f.coreStore.LoadState(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	result := []core.Entry{}
	cursor := ""
	for {
		page, err := f.coreStore.Scan(f.ctx, state.RolloutID, core.Query{SessionID: id, Cursor: cursor, Limit: core.DefaultPageSize, MaxBytes: 128 << 20})
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range page.Records {
			result = append(result, record.Entry)
		}
		if page.Next == "" {
			return result
		}
		cursor = page.Next
	}
}

func subagentTestRequest(t *testing.T, f *fixture, id string) core.Request {
	t.Helper()
	session, err := f.service.Core(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.Attempts().Scan(f.ctx, core.AttemptQuery{Purpose: "generation", Reverse: true, Limit: 1, MaxBytes: 128 << 20})
	if err != nil || len(page.Attempts) != 1 {
		t.Fatalf("no generation for %s: %+v %v", id, page, err)
	}
	request, err := session.Attempts().Request(f.ctx, page.Attempts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestSubagentSpawnPreservesPrefixHistoryAndCurrentComputer(t *testing.T) {
	f := newFixture(t, 2, func(context.Context, int, map[string]any) any { return textReply("Report ready.") })
	parent := subagentTestSeed(t, f)
	if err := f.service.SelectComputer(f.ctx, parent.id, f.daemons[1].Credentials.ComputerID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, parent.id, InputRequest{ID: "current-computer", Text: "Use the current computer for the delegated work."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, parent.id)
	parentRequest := subagentTestRequest(t, f, parent.id)
	parentState := parent.core.Load().State()
	parentPath, err := f.coreStore.Scan(f.ctx, parentState.RolloutID, core.Query{PathHead: parentState.Branch.HeadID, Limit: 1000, MaxBytes: 128 << 20})
	if err != nil || parentPath.Next != "" {
		t.Fatalf("capture parent history: %+v %v", parentPath, err)
	}
	for _, inherited := range []bool{true, false} {
		t.Run(fmt.Sprintf("inherit=%v", inherited), func(t *testing.T) {
			child := subagentTestSpawn(t, f, parent, fmt.Sprintf("spawn-%v", inherited), "Inspect the delegated source.", inherited)
			state := subagentTestFinished(t, f, child.SessionID)
			var record domain.Session
			if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
				t.Fatal(err)
			}
			if record.SubagentParentID != parent.id || record.ParentSessionID != "" || record.ComputerID != f.daemons[1].Credentials.ComputerID {
				t.Fatalf("wrong delegation ownership or Computer: %+v", record)
			}
			if (state.RolloutID == parentState.RolloutID) != inherited {
				t.Fatalf("wrong Rollout ownership: parent=%s child=%s inherited=%v", parentState.RolloutID, state.RolloutID, inherited)
			}
			request := subagentTestRequest(t, f, child.SessionID)
			if request.SystemPrompt != parentRequest.SystemPrompt || !reflect.DeepEqual(request.Tools, parentRequest.Tools) {
				t.Fatal("child request changed the parent's actual System or tool schema prefix")
			}
			identityFound := false
			for _, message := range request.Messages {
				if message.ContextKind != identityKind {
					continue
				}
				var identity identityMetadata
				if json.Unmarshal(message.Metadata, &identity) == nil && identity.SessionID == child.SessionID {
					identityFound = message.Role == core.RoleUser && identity.ParentID == parent.id
				}
			}
			if !identityFound {
				t.Fatal("child request lacks its own Host User identity")
			}
			if inherited {
				childPath, err := f.coreStore.Scan(f.ctx, state.RolloutID, core.Query{PathHead: state.Branch.HeadID, Limit: 1000, MaxBytes: 128 << 20})
				if err != nil || childPath.Next != "" {
					t.Fatalf("child history: %+v %v", childPath, err)
				}
				byID := map[string]core.Entry{}
				for _, record := range childPath.Records {
					byID[record.Entry.ID] = record.Entry
				}
				for _, original := range parentPath.Records {
					if copied, ok := byID[original.Entry.ID]; !ok || !reflect.DeepEqual(copied, original.Entry) {
						t.Fatalf("inherited entry %s disappeared or was rewritten", original.Entry.ID)
					}
				}
			}
			again, err := f.service.spawnSubagent(f.ctx, parent, core.Invocation{SessionID: parent.id, ExecutionID: fmt.Sprintf("spawn-%v", inherited)}, SpawnAgentInput{Task: "Inspect the delegated source.", InheritContext: inherited})
			if err != nil || again.SessionID != child.SessionID {
				t.Fatalf("spawn retry created another child: %+v %v", again, err)
			}
		})
	}
}

func TestSubagentControlReusesSessionAndStopsWithoutNewIMContext(t *testing.T) {
	entered := make(chan struct{})
	var childCalls atomic.Int32
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "You are a subagent of Session") && childCalls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return nil
		}
		return textReply("Work reported.")
	})
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "control-child", "Investigate the first topic.", false)
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	other, err := f.service.CreateSession(f.ctx, f.agent.ID, "Unrelated caller")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.controlSubagent(f.ctx, other.ID, "foreign", AgentControlInput{AgentID: child.SessionID, Input: "Take over"}); err == nil {
		t.Fatal("unrelated Session controlled a child")
	}
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "stop", AgentControlInput{AgentID: child.SessionID, Signal: "stop"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		state, err := f.coreStore.LoadState(f.ctx, child.SessionID)
		return err == nil && state.LastRun != nil && state.LastRun.Status == "cancelled"
	})
	observation := core.User("A new group message must stay in the root.")
	observation.ContextKind = "im.observation"
	observation.Metadata = encode(messages.Metadata{Source: "im", EventID: "new-background"})
	if _, err := f.service.Deliver(f.ctx, child.SessionID, observation, nil); err == nil {
		t.Fatal("child accepted a new IM observation")
	}
	if _, err := f.service.Receipt(f.ctx, child.SessionID, "new-background"); !domain.IsMissing(err) {
		t.Fatalf("rejected IM input left a receipt: %v", err)
	}
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "reuse", AgentControlInput{AgentID: child.SessionID, Input: "Continue with the second topic."}); err != nil {
		t.Fatal(err)
	}
	subagentTestFinished(t, f, child.SessionID)
	if childCalls.Load() != 2 {
		t.Fatalf("control did not reuse one child Run: calls=%d", childCalls.Load())
	}
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "reuse", AgentControlInput{AgentID: child.SessionID, Input: "Continue with the second topic."}); err != nil {
		t.Fatal(err)
	}
	if childCalls.Load() != 2 {
		t.Fatal("replaying one control input started work again")
	}
}

func TestInheritedSubagentUsesExplicitModelWithoutChangingParent(t *testing.T) {
	var usedOverride atomic.Bool
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if request["model"] == "fixture-next" {
			usedOverride.Store(true)
		}
		return textReply("Model-specific result.")
	})
	parent := subagentTestSeed(t, f)
	selection := parent.core.Load().Configuration().Selection
	override := anotherModel(t, f)
	child, err := f.service.spawnSubagent(f.ctx, parent, core.Invocation{SessionID: parent.id, ExecutionID: "override-model"}, SpawnAgentInput{Task: "Use the explicitly selected model.", InheritContext: true, Model: override.ID})
	if err != nil {
		t.Fatal(err)
	}
	subagentTestFinished(t, f, child.SessionID)
	session, err := f.service.Core(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Configuration().Selection.ID != override.ID || !usedOverride.Load() {
		t.Fatalf("inherited parent selection overrode requested child model: selection=%+v used=%v", session.Configuration().Selection, usedOverride.Load())
	}
	if !reflect.DeepEqual(parent.core.Load().Configuration().Selection, selection) {
		t.Fatal("child model override changed its parent's selection")
	}
}

func TestStopCancelsSubagentSpawnWaitingForTreeLock(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "You are a subagent of Session") {
			close(entered)
			select {
			case <-release:
				return toolReply("nested-spawn", "spawn_agent", map[string]any{"task": "Nested work waiting for the tree lock."})
			case <-ctx.Done():
				return nil
			}
		}
		return textReply("Root result.")
	})
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "locking-child", "Delegate one nested task.", false)
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	session, err := f.service.Core(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	events := session.Subscribe(128)
	defer events.Close()
	f.service.subagentMu.Lock()
	defer f.service.subagentMu.Unlock()
	releaseOnce.Do(func() { close(release) })
	for {
		select {
		case event := <-events.C:
			if event.Type == core.EventToolStart {
				goto dispatched
			}
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
	}
dispatched:
	if err := f.service.Stop(f.ctx, child.SessionID); err != nil {
		t.Fatal(err)
	}
	// Keep the lock held: cancellation must release the executing Core Run
	// independently, otherwise tree deletion deadlocks while joining it.
	eventually(t, f.ctx, func() bool {
		state := session.Snapshot()
		return !state.Running && state.State.LastRun != nil && state.State.LastRun.Status == "cancelled"
	})
	rows, err := f.store.List(f.ctx, "sessions")
	if err != nil || len(rows) != 2 {
		t.Fatalf("cancelled lock waiter created a grandchild: count=%d error=%v", len(rows), err)
	}
}

func TestSubagentProgressIsPassiveAndNaturalEndWakesWithFinalReport(t *testing.T) {
	childSecondRequest := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var childCalls, rootCalls atomic.Int32
	var rootSawFinal atomic.Bool
	f := newFixture(t, 1, func(ctx context.Context, _ int, request map[string]any) any {
		raw := string(encode(request))
		if strings.Contains(raw, "You are a subagent of Session") {
			if childCalls.Add(1) == 1 {
				response := toolReply("inspect", "exec_command", map[string]any{"cmd": testshell.CWD(), "login": false}).(map[string]any)
				response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] = "PROGRESS_REPORT_MARKER"
				return response
			}
			close(childSecondRequest)
			select {
			case <-release:
				return textReply("FINAL_REPORT_MARKER")
			case <-ctx.Done():
				return nil
			}
		}
		rootCalls.Add(1)
		if strings.Contains(raw, "naturally ended Run") {
			rootSawFinal.Store(strings.Contains(raw, "FINAL_REPORT_MARKER"))
		}
		return textReply("Root reviewed the evidence.")
	})
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "progress-child", "Inspect the current working directory.", false)
	select {
	case <-childSecondRequest:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	eventually(t, f.ctx, func() bool {
		for _, entry := range subagentTestEntries(t, f, parent.id) {
			if entry.Message != nil && entry.Message.ContextKind == subagentUpdateKind && strings.Contains(string(encode(entry.Message)), "PROGRESS_REPORT_MARKER") {
				return true
			}
		}
		return false
	})
	if rootCalls.Load() != 1 || parent.core.Load().Snapshot().Running {
		t.Fatal("ordinary child progress woke the root")
	}
	once.Do(func() { close(release) })
	state := subagentTestFinished(t, f, child.SessionID)
	eventually(t, f.ctx, func() bool { return rootCalls.Load() >= 2 && rootSawFinal.Load() })
	f.idle(t, parent.id)
	// Force recovery to re-read already delivered child history. Stable Core
	// receipts must suppress both report duplication and repeated activation.
	if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error {
		return tx.DeleteMaintenance(f.ctx, "subagent_delivery", child.SessionID)
	}); err != nil {
		t.Fatal(err)
	}
	before := rootCalls.Load()
	f.restart(t)
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	if rootCalls.Load() != before {
		t.Fatal("recovery reactivated an already handled child Run")
	}
	reports := 0
	for _, entry := range subagentTestEntries(t, f, parent.id) {
		if entry.Message != nil && entry.Message.ContextKind == subagentUpdateKind && strings.Contains(string(encode(entry.Message)), "FINAL_REPORT_MARKER") {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("final report was duplicated across recovery: %d", reports)
	}
}

func TestSubagentIdentityIsReassertedAfterCompaction(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("A concise summary and result.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "compact-child", strings.Repeat("Historical task details preserved for compaction. ", 2500), false)
	subagentTestFinished(t, f, child.SessionID)
	if err := f.service.Compact(f.ctx, child.SessionID, "Retain the delegated task."); err != nil {
		t.Fatal(err)
	}
	view, err := f.service.InspectContext(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Request.Messages) == 0 || view.Request.Messages[0].ContextKind != "summary" {
		t.Fatal("fixture did not actually compact")
	}
	identities := 0
	for _, message := range view.Request.Messages {
		if message.ContextKind != identityKind {
			continue
		}
		var metadata identityMetadata
		if json.Unmarshal(message.Metadata, &metadata) == nil && metadata.SessionID == child.SessionID && metadata.ParentID == parent.id && metadata.CompactionID != "" && message.Role == core.RoleUser {
			identities++
		}
	}
	if identities != 1 {
		t.Fatalf("post-compaction User identity count=%d, want 1", identities)
	}
	if err := f.service.InitializeSession(f.ctx, child.SessionID); err != nil {
		t.Fatal(err)
	}
	after, err := f.service.InspectContext(f.ctx, child.SessionID)
	if err != nil || len(after.Request.Messages) != len(view.Request.Messages) {
		t.Fatalf("environment refresh repeated identity: before=%d after=%d error=%v", len(view.Request.Messages), len(after.Request.Messages), err)
	}
}

func TestRootDeletionRemovesSharedAndIndependentSubagentDescendants(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Recorded result.") })
	parent := subagentTestSeed(t, f)
	shared := subagentTestSpawn(t, f, parent, "shared-child", "Shared-context child.", true)
	subagentTestFinished(t, f, shared.SessionID)
	independent := subagentTestSpawn(t, f, parent, "independent-child", "Independent child.", false)
	subagentTestFinished(t, f, independent.SessionID)
	sharedRuntime, err := f.service.open(f.ctx, shared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	grandchild := subagentTestLegacySpawn(t, f, sharedRuntime, "independent-grandchild", "Independent descendant of a shared child.", false)
	subagentTestFinished(t, f, grandchild.SessionID)
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "skip-generation", AgentControlInput{AgentID: grandchild.SessionID, Input: "Bypass the direct parent"}); err == nil {
		t.Fatal("root controlled a grandchild directly")
	}
	// A user Fork has historical provenance, not delegated ownership. Its
	// independent history and its own delegated tree must survive this deletion.
	f.idle(t, parent.id)
	fork, err := f.service.Fork(f.ctx, parent.id, parent.core.Load().Head(), "Independent ordinary fork", "retained-ordinary-fork")
	if err != nil {
		t.Fatal(err)
	}
	forkRuntime, err := f.service.open(f.ctx, fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	forkChild := subagentTestSpawn(t, f, forkRuntime, "retained-fork-child", "Work owned by the independent ordinary fork.", false)
	subagentTestFinished(t, f, forkChild.SessionID)
	f.idle(t, fork.ID)
	subagentRecoveryStopWorker(f)
	f.idle(t, parent.id)
	f.idle(t, fork.ID)
	retained := map[string]core.RuntimeState{}
	for _, id := range []string{fork.ID, forkChild.SessionID} {
		state, err := f.coreStore.LoadState(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		retained[id] = state
	}
	deletedRollouts := map[string]bool{}
	for _, id := range []string{parent.id, shared.SessionID, independent.SessionID, grandchild.SessionID} {
		state, err := f.coreStore.LoadState(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		deletedRollouts[state.RolloutID] = true
	}
	archived := true
	if err := f.service.SaveSessionPresentation(f.ctx, parent.id, nil, nil, &archived, nil); err != nil {
		t.Fatal(err)
	}
	var root domain.Session
	if err := f.store.Get(f.ctx, "sessions", parent.id, &root); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteSession(f.ctx, root.ID, root.Revision); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{root.ID, shared.SessionID, independent.SessionID, grandchild.SessionID} {
		var record domain.Session
		if err := f.store.Get(f.ctx, "sessions", id, &record); !domain.IsMissing(err) {
			t.Fatalf("deleted tree retained Host Session %s: %+v %v", id, record, err)
		}
		if _, err := f.coreStore.LoadState(f.ctx, id); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("deleted tree retained Core Session %s: %v", id, err)
		}
		var settings subagentSettings
		if err := f.store.Get(f.ctx, "subagent_settings", id, &settings); !domain.IsMissing(err) {
			t.Fatalf("deleted tree retained settings %s: %v", id, err)
		}
	}
	for rollout := range deletedRollouts {
		page, err := f.coreStore.Scan(f.ctx, rollout, core.Query{Limit: 1})
		if err != nil && !errors.Is(err, core.ErrNotFound) || len(page.Records) != 0 {
			t.Fatalf("deleted tree retained Rollout %s: %+v %v", rollout, page, err)
		}
	}
	for id, before := range retained {
		var record domain.Session
		if err := f.store.Get(f.ctx, "sessions", id, &record); err != nil || record.Deletion != nil {
			t.Fatalf("ordinary Fork tree lost Host Session %s: %+v %v", id, record, err)
		}
		after, err := f.coreStore.LoadState(f.ctx, id)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("ordinary Fork tree changed Core Session %s: before=%+v after=%+v error=%v", id, before, after, err)
		}
	}
}

func TestSubagentCompletionIncludesPendingFinalReportExactlyOnce(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var received atomic.Bool
	f := newFixture(t, 0, func(ctx context.Context, n int, request map[string]any) any {
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
		} else {
			raw := string(encode(request["messages"]))
			if strings.Contains(raw, "naturally ended Run") {
				for i := 0; i < 70; i++ {
					marker := fmt.Sprintf("REPORT_%03d_END", i)
					if count := strings.Count(raw, marker); count != 1 {
						t.Errorf("report %s occurred %d times in completion request, want 1", marker, count)
					}
				}
				received.Store(true)
			}
		}
		return textReply("Root response.")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "busy-root", Text: "Keep working while progress arrives."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	child := domain.Session{Meta: domain.Meta{ID: "transport-child"}, AgentID: f.agent.ID, SubagentParentID: f.session.ID, Title: "Progress source"}
	if err := f.store.Save(f.ctx, "sessions", &child.Meta, &child); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 70; i++ {
		message := core.Message{Role: core.RoleAssistant, Content: core.Text(fmt.Sprintf("REPORT_%03d_END", i))}
		entry := core.Entry{ID: fmt.Sprintf("report-%03d", i), RunID: "child-run", Kind: core.EntryMessage, Message: &message}
		if err := f.service.deliverSubagentEntry(f.ctx, child, entry); err != nil {
			t.Fatal(err)
		}
	}
	end := core.Entry{ID: "run-end", RunID: "child-run", Kind: core.EntryRun, Run: &core.RunRecord{ID: "child-run", Status: "completed"}}
	if err := f.service.deliverSubagentEntry(f.ctx, child, end); err != nil && !errors.Is(err, errSubagentReportsPending) {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	eventually(t, f.ctx, func() bool {
		err := f.service.deliverSubagentEntry(f.ctx, child, end)
		if err != nil && !errors.Is(err, errSubagentReportsPending) {
			t.Fatal(err)
		}
		return err == nil
	})
	eventually(t, f.ctx, func() bool { return received.Load() })
	f.idle(t, f.session.ID)
	// This test focuses on the Host delivery protocol; the fake child has no
	// runnable settings and cannot generate unrelated background notifications.
}

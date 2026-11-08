package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/schedule"
	testshell "github.com/oines/astral/internal/testkit/shell"
	"github.com/oines/astral/internal/wake"
)

func archiveSet(t *testing.T, f *fixture, id string, archived bool) {
	t.Helper()
	if err := f.service.SaveSessionPresentation(f.ctx, id, nil, nil, &archived, nil); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveStopsExecutionClearsDriversAndRetainsPassiveContext(t *testing.T) {
	entered := make(chan struct{})
	f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			<-ctx.Done()
			return nil
		}
		return textReply("A new explicit task.")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "running", Text: "Hold this task."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.core.Load().Steer(f.ctx, core.Input{ID: "pending-steer", Message: core.User("Must not resume.")}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.core.Load().FollowUp(f.ctx, core.Input{ID: "pending-follow", Message: core.User("Also must not resume.")}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.core.Load().InjectContext(f.ctx, core.Input{ID: "passive", Contexts: []core.Message{core.User("Already accepted background information.")}}); err != nil {
		t.Fatal(err)
	}
	archiveSet(t, f, f.session.ID, true)
	state, err := f.coreStore.LoadState(f.ctx, f.session.ID)
	if err != nil || state.LastRun == nil || state.LastRun.Status != "cancelled" || state.PendingInputs[core.InputSteer]+state.PendingInputs[core.InputFollowUp] != 0 {
		t.Fatalf("archive did not stop and clear driving work: %+v %v", state, err)
	}
	for _, id := range []string{"pending-steer", "pending-follow"} {
		receipt, err := f.coreStore.LookupInput(f.ctx, f.session.ID, id)
		if err != nil || receipt.State != core.InputCleared {
			t.Fatalf("input %s was not cleared: %+v %v", id, receipt, err)
		}
	}
	if _, err := f.coreStore.LookupInput(f.ctx, f.session.ID, "passive"); err != nil {
		t.Fatal("archive erased accepted passive context:", err)
	}
	f.service.mu.Lock()
	_, loaded := f.service.runtimes[f.session.ID]
	f.service.mu.Unlock()
	if loaded {
		t.Fatal("archive retained the live execution runtime")
	}
	for _, action := range []func() error{
		func() error {
			_, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "blocked", Text: "Do not execute."})
			return err
		},
		func() error { return f.service.Continue(f.ctx, f.session.ID) },
		func() error { return f.service.Compact(f.ctx, f.session.ID, "") },
		func() error { return f.service.ResumeContext(f.ctx, f.session.ID) },
		func() error {
			return f.service.deliverSchedule(f.ctx, schedule.Occurrence{AgentID: f.agent.ID, SessionID: f.session.ID, ScheduleID: "blocked", ScheduledAt: time.Now(), Prompt: "Do not execute."})
		},
		func() error {
			_, err := f.service.deliverWake(f.ctx, wake.Batch{ID: "blocked", Digest: "blocked", AgentID: f.agent.ID, SessionID: f.session.ID, Events: []wake.BatchEvent{{Text: "Do not execute."}}})
			return err
		},
	} {
		if err := action(); !persistence.IsSessionArchived(err) {
			t.Fatalf("sealed execution returned %v", err)
		}
	}
	view, err := f.service.View(f.ctx, f.session.ID)
	if err != nil || !view.Archived || view.CanContinue {
		t.Fatalf("sealed history was not readable and readonly: %+v %v", view, err)
	}
	archiveSet(t, f, f.session.ID, false)
	if f.count() != 1 {
		t.Fatal("restore restarted old work")
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "new-task", Text: "Start a new task."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if f.count() != 2 {
		t.Fatal("restore did not allow an explicit new task")
	}
}

func TestArchiveColdTreeStaysColdAndRestoreDoesNotStartInitialTask(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("archived work reached the model")
		return textReply("unexpected")
	})
	parent, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	subagentRecoveryStopWorker(f)
	child := subagentTestSpawn(t, f, parent, "cold-archive", "Unstarted delegated task.", false)
	archiveSet(t, f, f.session.ID, true)
	f.restart(t)
	var childRecord domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &childRecord); err != nil || !childRecord.Archived {
		t.Fatalf("tree was not sealed: %+v %v", childRecord, err)
	}
	if err := f.service.reconcileSubagent(f.ctx, childRecord); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileModelFallback(f.ctx, child.SessionID); err != nil {
		t.Fatal(err)
	}
	f.service.mu.Lock()
	count := len(f.service.runtimes)
	f.service.mu.Unlock()
	if count != 0 || f.count() != 0 {
		t.Fatalf("archived recovery opened history: runtimes=%d calls=%d", count, f.count())
	}
	for _, operation := range []func() error{
		func() error {
			_, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "cold-blocked", Text: "Blocked."})
			return err
		},
		func() error { return f.service.Continue(f.ctx, f.session.ID) },
		func() error { return f.service.Compact(f.ctx, f.session.ID, "") },
	} {
		if err := operation(); !persistence.IsSessionArchived(err) {
			t.Fatalf("cold write was not rejected: %v", err)
		}
	}
	f.service.mu.Lock()
	count = len(f.service.runtimes)
	f.service.mu.Unlock()
	if count != 0 || f.count() != 0 {
		t.Fatalf("rejected cold write opened history: runtimes=%d calls=%d", count, f.count())
	}
	if _, err := f.service.Summary(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	archiveSet(t, f, f.session.ID, false)
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &childRecord); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileSubagent(f.ctx, childRecord); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coreStore.LoadState(f.ctx, child.SessionID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("restore initialized an old unstarted child: %v", err)
	}
	if f.count() != 0 {
		t.Fatal("restore resumed an old delegated task")
	}
}

func TestArchiveInterruptedIntentIsRemovedByDeletionAndReplayIgnoresOrphans(t *testing.T) {
	for _, bot := range []bool{false, true} {
		t.Run(map[bool]string{false: "session", true: "bot"}[bot], func(t *testing.T) {
			f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("unused") })
			archiveSet(t, f, f.session.ID, true)
			intent := archiveCleanup{AgentID: f.agent.ID, RootID: f.session.ID, IDs: []string{f.session.ID}}
			if err := f.store.SaveMaintenance(f.ctx, archiveCleanupKind, f.session.ID, "pending", intent); err != nil {
				t.Fatal(err)
			}
			if bot {
				if err := f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err != nil {
					t.Fatal(err)
				}
			} else {
				var record domain.Session
				if err := f.store.Get(f.ctx, "sessions", f.session.ID, &record); err != nil {
					t.Fatal(err)
				}
				if err := f.service.DeleteSession(f.ctx, record.ID, record.Revision); err != nil {
					t.Fatal(err)
				}
			}
			var pending archiveCleanup
			if err := f.store.Maintenance(f.ctx, archiveCleanupKind, f.session.ID, &pending); !domain.IsMissing(err) {
				t.Fatalf("delete retained archive intent: %v", err)
			}
			// A legacy orphan must also be harmless instead of blocking startup.
			if err := f.store.SaveMaintenance(f.ctx, archiveCleanupKind, f.session.ID, "pending", intent); err != nil {
				t.Fatal(err)
			}
			if err := f.service.ResumeSessionArchives(f.ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestApplyModelPrioritySkipsArchivedAndLegacyArchivedTrees(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("bulk model selection executed work")
		return textReply("unused")
	})
	parent, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	subagentRecoveryStopWorker(f)
	child := subagentTestSpawn(t, f, parent, "legacy-bulk", "Remain unstarted.", false)
	originalModel := f.agent.DefaultModelID
	// Simulate an old Host's presentation-only archive: child is still unmarked.
	root := f.session
	root.Archived = true
	if err := f.store.Save(f.ctx, "sessions", &root.Meta, &root); err != nil {
		t.Fatal(err)
	}
	if err := parent.closeCore(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.service.removeDeletedRuntime(parent.id)
	active, err := f.service.CreateSession(f.ctx, f.agent.ID, "Active root")
	if err != nil {
		t.Fatal(err)
	}
	replacement := anotherModel(t, f)
	f.agent.ModelPriority = []core.ModelSelection{{ID: replacement.ID}}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	updated, err := f.service.ApplyAgentModelPriority(f.ctx, f.agent.ID)
	if err != nil || updated != 1 {
		t.Fatalf("bulk model selection did not skip sealed tree: updated=%d err=%v", updated, err)
	}
	f.service.mu.Lock()
	rootLoaded, childLoaded := f.service.runtimes[root.ID] != nil, f.service.runtimes[child.SessionID] != nil
	f.service.mu.Unlock()
	if rootLoaded || childLoaded {
		t.Fatal("bulk model selection opened archived history")
	}
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil || settings.Configuration.Selection.ID != originalModel {
		t.Fatalf("bulk selection changed archived child: %+v %v", settings, err)
	}
	selected, err := f.service.ModelSelection(f.ctx, active.ID)
	if err != nil || selected.ID != replacement.ID {
		t.Fatalf("active root did not update: %+v %v", selected, err)
	}
}

func TestArchiveCancelsDelegatedRunAndSuppressesOldReportsAfterRestore(t *testing.T) {
	entered := make(chan struct{})
	f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 2 {
			close(entered)
			<-ctx.Done()
			return nil
		}
		return textReply("Done.")
	})
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "archive-running-child", "Hold the delegated generation.", false)
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	archiveSet(t, f, parent.id, true)
	for _, id := range []string{parent.id, child.SessionID} {
		var record domain.Session
		if err := f.store.Get(f.ctx, "sessions", id, &record); err != nil || !record.Archived {
			t.Fatalf("member was not sealed: %s %+v %v", id, record, err)
		}
		f.service.mu.Lock()
		loaded := f.service.runtimes[id] != nil
		f.service.mu.Unlock()
		if loaded {
			t.Fatalf("member retained live runtime: %s", id)
		}
	}
	state, err := f.coreStore.LoadState(f.ctx, child.SessionID)
	if err != nil || state.LastRun == nil || state.LastRun.Status != "cancelled" {
		t.Fatalf("delegated work was not cancelled: %+v %v", state.LastRun, err)
	}
	archiveSet(t, f, parent.id, false)
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if f.count() != 2 {
		t.Fatal("restoring the tree replayed its old task or reports")
	}
	parent, err = f.service.open(f.ctx, parent.id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "new-after-restore", AgentControlInput{AgentID: child.SessionID, Input: "An explicitly delegated new task."}); err != nil {
		t.Fatal(err)
	}
	subagentTestFinished(t, f, child.SessionID)
}

func TestArchiveSuppressesColdInterruptedRunUntilExplicitNewTask(t *testing.T) {
	var explicit atomic.Int32
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "EXPLICIT_NEW_CHILD_TASK") {
			explicit.Add(1)
		}
		return textReply("Done.")
	})
	parent, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	subagentRecoveryStopWorker(f)
	child := subagentTestSpawn(t, f, parent, "cold-started-archive", "An old interrupted task.", false)
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.core.Load().Steer(f.ctx, core.Input{ID: "subagent_initial_" + child.SessionID, Message: core.User("An old interrupted task.")}); err != nil {
		t.Fatal(err)
	}
	state := r.core.Load().State()
	const oldRun = "archive-interrupted-before-freeze"
	_, err = f.coreStore.Commit(f.ctx, child.SessionID, core.Commit{ID: "archive-started-boundary", ExpectedVersion: state.Version, Entries: []core.Entry{{ID: "archive-run-start", SessionID: child.SessionID, Version: state.Version + 1, Time: time.Now().UTC(), Kind: core.EntryRun, RunID: oldRun, Run: &core.RunRecord{ID: oldRun, Status: "started"}}}})
	if err != nil {
		t.Fatal(err)
	}
	// Old versions archived only the root's presentation. New Host recovery
	// must notice that ancestor before opening the child's interrupted history.
	root := f.session
	root.Archived = true
	if err := f.store.Save(f.ctx, "sessions", &root.Meta, &root); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileModelFallback(f.ctx, child.SessionID); err != nil {
		t.Fatal(err)
	}
	f.service.mu.Lock()
	loaded := len(f.service.runtimes)
	f.service.mu.Unlock()
	if loaded != 0 || f.count() != 0 {
		t.Fatal("legacy archive resumed or opened the interrupted child")
	}
	archiveSet(t, f, root.ID, false)
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil || settings.ArchiveStoppedRunID != oldRun {
		t.Fatalf("cold stop identity was not retained: %+v %v", settings, err)
	}
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if f.count() != 0 {
		t.Fatal("restore automatically continued a cold old Run")
	}
	if _, err := f.service.controlSubagent(f.ctx, root.ID, "cold-explicit-new", AgentControlInput{AgentID: child.SessionID, Input: "EXPLICIT_NEW_CHILD_TASK"}); err != nil {
		t.Fatal(err)
	}
	subagentTestFinished(t, f, child.SessionID)
	if explicit.Load() < 1 {
		t.Fatal("the stopped-run marker blocked a new explicit instruction")
	}
}

func TestArchiveClosesManagedProcessAndRetiresItsHandle(t *testing.T) {
	f := newFixture(t, 1, func(_ context.Context, n int, _ map[string]any) any {
		if n == 1 {
			return toolReply("archive-process", "exec_command", map[string]any{"cmd": testshell.Sleep(time.Minute), "login": false, "yield_time_ms": 250})
		}
		return textReply("Keep the yielded process owned by this Session.")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "process-task", Text: "Start a managed process."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.processMu.Lock()
	processes := len(r.processes)
	r.processMu.Unlock()
	if processes != 1 {
		t.Fatalf("fixture did not retain a managed process: %d", processes)
	}
	rows, err := f.store.List(f.ctx, "process_handles")
	if err != nil || len(rows) != 1 {
		t.Fatalf("fixture did not expose its process handle: %d %v", len(rows), err)
	}
	var original processHandle
	if err := json.Unmarshal(rows[0].Data, &original); err != nil {
		t.Fatal(err)
	}
	archiveSet(t, f, f.session.ID, true)
	eventually(t, f.ctx, func() bool { r.processMu.Lock(); defer r.processMu.Unlock(); return len(r.processes) == 0 })
	var retired processHandle
	if err := f.store.Get(f.ctx, "process_handles", original.ID, &retired); err != nil || !retired.Retired {
		t.Fatalf("archive retained a usable old process handle: %+v %v", retired, err)
	}
	archiveSet(t, f, f.session.ID, false)
	current, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := strconv.Atoi(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = (hostTool{runtime: current}).translate(f.ctx, core.Invocation{SessionID: f.session.ID, Call: core.ToolCall{Name: "write_stdin", Arguments: encode(map[string]int{"session_id": id})}})
	if err == nil {
		t.Fatal("restoring the Session reused a retired process handle")
	}
}

func TestArchiveSerializesExistingAdmissionAndConcurrentSpawn(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("sealed work started a generation")
		return textReply("unexpected")
	})
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	subagentRecoveryStopWorker(f)
	r.mu.Lock()
	archiveDone := make(chan error, 1)
	go func() {
		sealed := true
		archiveDone <- f.service.SaveSessionPresentation(f.ctx, r.id, nil, nil, &sealed, nil)
	}()
	eventually(t, f.ctx, func() bool {
		if f.service.subagentMu.TryLock() {
			f.service.subagentMu.Unlock()
			return false
		}
		return true
	})
	spawnDone := make(chan error, 1)
	go func() {
		_, err := f.service.spawnSubagent(f.ctx, r, core.Invocation{SessionID: r.id, ExecutionID: "spawn-at-archive"}, SpawnAgentInput{Task: "Must not escape the sealed tree."})
		spawnDone <- err
	}()
	var before domain.Session
	if err := f.store.Get(f.ctx, "sessions", r.id, &before); err != nil || before.Archived {
		r.mu.Unlock()
		t.Fatalf("archive crossed an existing admission: %+v %v", before, err)
	}
	// This admission already owns the same lock before the archive commit.
	// It is accepted first, then must be cleared by the sealed-tree cleanup.
	_, err = r.core.Load().Steer(f.ctx, core.Input{ID: "admitted-before-archive", Message: core.User("Already admitted, not yet started.")})
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-archiveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	select {
	case err := <-spawnDone:
		if !persistence.IsSessionArchived(err) {
			t.Fatalf("a concurrent Spawn escaped archive: %v", err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	receipt, err := f.coreStore.LookupInput(f.ctx, r.id, "admitted-before-archive")
	if err != nil || receipt.State != core.InputCleared {
		t.Fatalf("archive retained a pre-commit driving input: %+v %v", receipt, err)
	}
	rows, err := f.store.List(f.ctx, "sessions")
	if err != nil || len(rows) != 1 || f.count() != 0 {
		t.Fatalf("archive leaked new work: sessions=%d calls=%d err=%v", len(rows), f.count(), err)
	}
}

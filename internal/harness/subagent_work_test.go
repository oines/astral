package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestSubagentWorkRoutesOnlyChangedChildrenAndWaitingParents(t *testing.T) {
	work := newSubagentWork()
	for i := range 35 {
		work.observe(domain.Session{Meta: domain.Meta{ID: fmt.Sprint("child-", i)}, SubagentParentID: "parent"}, nil)
	}
	if got := work.affected(map[string]struct{}{"parent": {}, "ordinary-root": {}}); len(got) != 0 {
		t.Fatalf("settled siblings were selected for root work: %v", got)
	}
	child := domain.Session{Meta: domain.Meta{ID: "child-0"}, SubagentParentID: "parent"}
	work.observe(child, errSubagentReportsPending)
	work.observe(child, core.ErrBusy)
	got := work.affected(map[string]struct{}{"parent": {}, "child-1": {}})
	if len(got) != 2 {
		t.Fatalf("affected=%v", got)
	}
	for _, id := range []string{"child-0", "child-1"} {
		if _, ok := got[id]; !ok {
			t.Fatalf("missing %s", id)
		}
	}
	work.observe(child, nil)
	if len(work.affected(map[string]struct{}{"parent": {}})) != 0 {
		t.Fatal("successful delivery retained parent waiting work")
	}
	work.observe(child, errSubagentReportsPending)
	work.forget(child.ID)
	if len(work.affected(map[string]struct{}{"parent": {}, child.ID: {}})) != 0 {
		t.Fatal("deleted child retained work")
	}
}

func TestSubagentDirtyWorkCoalescesWithoutLosingConcurrentIDs(t *testing.T) {
	s := &Service{subagentWake: make(chan struct{}, 1)}
	var producers sync.WaitGroup
	for worker := range 4 {
		producers.Add(1)
		go func(worker int) {
			defer producers.Done()
			for i := range 32 {
				s.wakeSubagents(fmt.Sprintf("%d-%d", worker, i))
			}
		}(worker)
	}
	seen := map[string]bool{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for len(seen) < 128 {
		select {
		case <-s.subagentWake:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		dirty, full := s.takeSubagentWork()
		if full {
			t.Fatal("ID wake requested a global scan")
		}
		for id := range dirty {
			seen[id] = true
		}
	}
	producers.Wait()
	s.wakeSubagents("")
	if dirty, full := s.takeSubagentWork(); len(dirty) != 0 || full {
		t.Fatal("empty notification caused subagent work")
	}
	s.wakeSubagents()
	if _, full := s.takeSubagentWork(); !full {
		t.Fatal("explicit recovery lost its full scan")
	}
}

func TestSubagentWorkerDoesNotReadSettledChildrenForEitherRoot(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Completed.") })
	parent := subagentTestSeed(t, f)
	children := map[string]bool{}
	for i := range 2 {
		child := subagentTestSpawn(t, f, parent, fmt.Sprint("selective-", i), "Complete bounded child work.", false)
		state := subagentTestFinished(t, f, child.SessionID)
		subagentWorkerCheckpoint(t, f, child.SessionID, state)
		children[child.SessionID] = true
	}
	f.idle(t, parent.id)
	other, err := f.service.CreateSession(f.ctx, f.agent.ID, "Unrelated root")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	counted := &subagentWorkerStore{Store: f.coreStore, childIDs: children, loadTarget: 2, loaded: make(chan struct{})}
	f.service = New(f.store, counted, f.service.Models, f.hub)
	select {
	case <-counted.loaded:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	f.service.subagentMu.Lock()
	reads := counted.stateReads.Load()
	f.service.subagentMu.Unlock()
	for _, id := range []string{parent.id, other.ID} {
		for i := range 2 {
			if _, err := f.service.Submit(f.ctx, id, InputRequest{ID: fmt.Sprintf("selective-root-%s-%d", id, i), Text: "Complete this independent root request."}); err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if after := counted.stateReads.Load(); after != reads {
		t.Fatalf("root work reread settled child states: %d -> %d", reads, after)
	}
}

func TestColdSubagentWaitingReportRoutesParentApplicationAndClearsAfterDelivery(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "COLD_PARENT_BLOCK") {
			select {
			case <-entered:
			default:
				close(entered)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
		}
		return textReply("A cold-child report.")
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	parent := subagentTestSeed(t, f)
	subagentRecoveryStopWorker(f)
	child := subagentTestSpawn(t, f, parent, "cold-waiting-parent", "Complete before the busy parent receives any report.", false)
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	err = r.startSubagentLocked(f.ctx)
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	state := subagentTestFinished(t, f, child.SessionID)
	if err := f.service.closeDeletedRuntime(f.ctx, child.SessionID); err != nil {
		t.Fatal(err)
	}
	f.service.removeDeletedRuntime(child.SessionID)
	if _, err := f.service.Submit(f.ctx, parent.id, InputRequest{ID: "cold-parent-busy", Text: "COLD_PARENT_BLOCK"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	err = f.service.reconcileSubagent(f.ctx, record)
	if !errors.Is(err, errSubagentReportsPending) {
		t.Fatalf("cold child lost pending delivery: %v", err)
	}
	if _, hot := f.service.runtimes[child.SessionID]; hot {
		t.Fatal("waiting reports opened the settled cold child")
	}
	work := newSubagentWork()
	work.observe(record, err)
	if len(work.affected(map[string]struct{}{parent.id: {}})) != 1 {
		t.Fatal("busy parent did not retain its waiting child")
	}
	f.service.takeSubagentWork() // Discard setup events before the actual parent progress.
	releaseOnce.Do(func() { close(release) })
	f.idle(t, parent.id)
	dirty, full := f.service.takeSubagentWork()
	if _, ok := dirty[parent.id]; !ok || full {
		t.Fatal("Core parent application did not signal its ID")
	}
	if err := f.service.reconcileSubagentIDs(f.ctx, work, dirty); err != nil {
		t.Fatal(err)
	}
	if len(work.affected(map[string]struct{}{parent.id: {}})) != 0 {
		t.Fatal("successful terminal delivery kept waiting state")
	}
	f.idle(t, parent.id)
	receipt, err := f.coreStore.LookupInput(f.ctx, parent.id, "subagent_run_end_"+child.SessionID+"_"+state.LastRun.ID)
	if err != nil || receipt.State != core.InputApplied {
		t.Fatalf("terminal was not applied: %+v %v", receipt, err)
	}
}

func TestSubagentAcceptedTerminalRemainsWaitingWhileParentOnlyMaintains(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if _, generation := request["tools"]; !generation {
			select {
			case <-entered:
			default:
				close(entered)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
			return textReply("Retained parent summary.")
		}
		return textReply("Completed child work.")
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	parent := subagentTestSeed(t, f)
	if _, err := f.service.Submit(f.ctx, parent.id, InputRequest{ID: "parent-before-maintenance", Text: strings.Repeat("Retain this parent conversation for compaction. ", 2500)}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, parent.id)
	subagentRecoveryStopWorker(f)
	child := subagentTestSpawn(t, f, parent, "pending-terminal-maintenance", "Complete before parent compaction.", false)
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	err = r.startSubagentLocked(f.ctx)
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	state := subagentTestFinished(t, f, child.SessionID)
	var terminal core.Entry
	for _, entry := range subagentTestEntries(t, f, child.SessionID) {
		if entry.Run != nil && entry.Run.ID == state.LastRun.ID && entry.Run.Status == "completed" {
			terminal = entry
		}
	}
	if terminal.ID == "" {
		t.Fatal("no terminal source fact")
	}
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	compacted := make(chan error, 1)
	go func() {
		// The recovery fixture stopped the worker by cancelling Service.ctx.
		// Exercise the same Core maintenance and post-return Host notification
		// with the test's live context, without restarting that worker.
		_, err := parent.core.Load().Compact(ctx, "Keep the parent task.")
		f.service.notify(parent.id)
		compacted <- err
	}()
	select {
	case <-entered:
	case err := <-compacted:
		t.Fatalf("compaction did not reach model: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	inputID := "subagent_run_end_" + child.SessionID + "_" + terminal.RunID
	hash := digest([]any{child.SessionID, terminal.ID, terminal.RunID, terminal.Time.UTC(), "", true})
	if receipt, err := parent.core.Load().FollowUp(ctx, core.Input{ID: inputID, Digest: hash, Message: core.User("Accepted terminal runtime event.")}); err != nil || receipt.State != core.InputPending {
		t.Fatalf("pending terminal fixture: %+v %v", receipt, err)
	}
	var record domain.Session
	if err := f.store.Get(ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	err = f.service.deliverSubagentEntry(ctx, record, terminal)
	if !errors.Is(err, errSubagentReportsPending) {
		t.Fatalf("accepted but inactive terminal was acknowledged: %v", err)
	}
	work := newSubagentWork()
	work.observe(record, err)
	f.service.takeSubagentWork()
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-compacted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	dirty, _ := f.service.takeSubagentWork()
	if _, ok := dirty[parent.id]; !ok {
		t.Fatal("maintenance release did not wake the parent ID")
	}
	if err := f.service.reconcileSubagentIDs(ctx, work, dirty); err != nil {
		t.Fatal(err)
	}
	f.idle(t, parent.id)
	receipt, err := f.coreStore.LookupInput(ctx, parent.id, inputID)
	if err != nil || receipt.State != core.InputApplied || len(work.waiting) != 0 {
		t.Fatalf("terminal did not finish after maintenance: %+v wait=%v err=%v", receipt, work.waiting, err)
	}
}

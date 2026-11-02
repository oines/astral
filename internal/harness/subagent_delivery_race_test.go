package harness

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestSubagentReportDeliverySerializesWithDeletionAndCannotResurrectWatermark(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Committed report.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "delivery-delete-race", "Complete before the delivery admission race.", false)
	state := subagentTestFinished(t, f, child.SessionID)
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	f.idle(t, parent.id)
	subagentRecoveryStopWorker(f)
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	var releaseOnce sync.Once
	var operations sync.WaitGroup
	parent.mu.Lock()
	releaseParent := func() { releaseOnce.Do(parent.mu.Unlock) }
	t.Cleanup(func() {
		releaseParent()
		cancel()
		joined := make(chan struct{})
		go func() { operations.Wait(); close(joined) }()
		select {
		case <-joined:
		case <-time.After(3 * time.Second):
			t.Error("delivery/deletion operations did not leave their bounded admission wait")
		}
	})
	message := core.Message{Role: core.RoleAssistant, Content: core.Text("REPORT_ADMITTED_BEFORE_CHILD_DELETION")}
	entry := core.Entry{ID: "delayed-report", RunID: "delayed-run", Kind: core.EntryMessage, Message: &message}
	delivered := make(chan error, 1)
	operations.Add(1)
	go func() {
		defer operations.Done()
		delivered <- f.service.deliverSubagentEntry(ctx, record, entry)
	}()
	// No Host worker remains. Failure to acquire the tree lock proves this
	// delivery owns admission and is now waiting for the parent's runtime lock.
	eventually(t, ctx, func() bool {
		if f.service.subagentMu.TryLock() {
			f.service.subagentMu.Unlock()
			return false
		}
		return true
	})
	deleted := make(chan error, 1)
	deletionStarted := make(chan struct{})
	operations.Add(1)
	go func() {
		defer operations.Done()
		close(deletionStarted)
		deleted <- f.service.deleteSubagent(ctx, parent.id, child.SessionID)
	}()
	<-deletionStarted
	select {
	case err := <-deleted:
		t.Fatalf("deletion crossed a prior delivery's admission boundary: %v", err)
	default:
	}
	releaseParent()
	for name, result := range map[string]<-chan error{"delivery": delivered, "deletion": deleted} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("%s failed after the ordered admission: %v", name, err)
			}
		case <-ctx.Done():
			t.Fatalf("%s did not finish: %v", name, ctx.Err())
		}
	}
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); !domain.IsMissing(err) {
		t.Fatalf("child deletion did not complete: %v", err)
	}
	reports := func() []string {
		result := []string{}
		for _, entry := range subagentTestEntries(t, f, parent.id) {
			if entry.Message != nil && entry.Message.ContextKind == subagentUpdateKind {
				result = append(result, entry.ID)
			}
		}
		return result
	}
	before, calls := reports(), f.count()
	for _, stale := range []core.Entry{
		entry,
		{ID: "deleted-child-run-end", RunID: "deleted-child-run", Kind: core.EntryRun, Run: &core.RunRecord{ID: "deleted-child-run", Status: "completed"}},
	} {
		if err := f.service.deliverSubagentEntry(f.ctx, record, stale); err == nil {
			t.Fatal("deleted child delivered a stale report or natural-end wakeup")
		}
	}
	if err := f.service.reconcileSubagent(f.ctx, record); err == nil {
		t.Fatal("a stale child record resumed reconciliation after deletion")
	}
	if err := f.service.reconcileSubagents(f.ctx); err != nil {
		t.Fatal(err)
	}
	if after := reports(); !reflect.DeepEqual(after, before) || f.count() != calls {
		t.Fatalf("deleted child appended reports or woke its parent: before=%v after=%v calls=%d/%d", before, after, calls, f.count())
	}
	for _, kind := range []string{"subagent_delivery", "subagent_activation"} {
		var maintenance any
		if err := f.store.Maintenance(f.ctx, kind, child.SessionID, &maintenance); !domain.IsMissing(err) {
			t.Fatalf("deleted child resurrected %s maintenance: %v", kind, err)
		}
	}
}

package harness

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
)

type subagentShutdownPendingStore struct {
	core.Store
	parentID  string
	entered   chan struct{}
	cancelled chan struct{}
	release   <-chan struct{}
	blocked   atomic.Bool
}

func (s *subagentShutdownPendingStore) Pending(ctx context.Context, id string, query core.PendingQuery) (core.Page, error) {
	page, err := s.Store.Pending(ctx, id, query)
	if err != nil || id != s.parentID || query.Kind != core.EntryInputQueued || query.InputKind != core.InputContext || len(page.Records) != 0 || !s.blocked.CompareAndSwap(false, true) {
		return page, err
	}
	// The real terminal-delivery check has read an empty pending page. Keep
	// that successful result in flight across cancellation, as a storage call
	// which has already completed its database read is allowed to do.
	close(s.entered)
	select {
	case <-ctx.Done():
		close(s.cancelled)
	case <-s.release:
		return page, nil
	}
	<-s.release
	return page, nil
}

func TestSubagentShutdownJoinsTerminalDeliveryBeforeClosingCoreHandles(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("A real completed child result.") })
	parent := subagentTestSeed(t, f)
	parentCore := parent.core.Load()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	store := &subagentShutdownPendingStore{Store: f.coreStore, parentID: parent.id, entered: make(chan struct{}), cancelled: make(chan struct{}), release: release}
	// Only Host delivery uses this wrapper for the already-open parent. Its
	// own Core Store stays unchanged, so Core.Close has no artificial blocker.
	f.service.subagentMu.Lock()
	f.service.CoreStore = store
	f.service.subagentMu.Unlock()
	child := subagentTestSpawn(t, f, parent, "shutdown-terminal-child", "Finish and notify the waiting parent.", false)
	select {
	case <-store.entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	state, err := f.coreStore.LoadState(f.ctx, child.SessionID)
	if err != nil || state.LastRun == nil || state.LastRun.Status != "completed" {
		t.Fatalf("store barrier did not intercept a real terminal child delivery: %+v %v", state.LastRun, err)
	}
	closeCtx, closeCancel := context.WithTimeout(f.ctx, 250*time.Millisecond)
	defer closeCancel()
	closed := make(chan error, 1)
	go func() { closed <- f.service.Close(closeCtx) }()
	select {
	case <-store.cancelled:
	case <-f.ctx.Done():
		t.Fatal("shutdown did not cancel its in-flight terminal reconciler")
	}
	// Waiting for this bounded Close to return removes scheduler timing from
	// the assertion: an implementation which closes Core first has already
	// cleared the parent's window before timing out on the blocked worker.
	select {
	case err := <-closed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked worker did not bound shutdown by its context: %v", err)
		}
	case <-f.ctx.Done():
		t.Fatal("shutdown ignored its context while joining the worker")
	}
	view, err := parentCore.ReadContext(f.ctx)
	if err != nil || view.ConfigurationID == "" || view.Selection.ID != f.agent.DefaultModelID {
		t.Fatalf("shutdown cleared a Core handle still owned by terminal delivery: selection=%+v configuration=%s error=%v", view.Selection, view.ConfigurationID, err)
	}
	unblock()
	cleanupCtx, cleanupCancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cleanupCancel()
	if err := f.service.Close(cleanupCtx); err != nil {
		t.Fatalf("shutdown could not finish after terminal delivery returned: %v", err)
	}
	if _, err := parentCore.ReadContext(f.ctx); !errors.Is(err, core.ErrClosed) {
		t.Fatalf("completed shutdown did not close its Core handles: %v", err)
	}
}

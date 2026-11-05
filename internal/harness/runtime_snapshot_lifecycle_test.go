package harness

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestRuntimeRetirementBlocksReadersBeforeJoiningCore(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("lifecycle reads called the model")
		return nil
	})
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Model a callback already holding the admission lock. Retirement must
	// block new snapshots immediately, then wait for that callback before Close.
	r.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- f.service.closeDeletedRuntime(f.ctx, r.id) }()
	eventually(t, f.ctx, func() bool {
		r.snapshotMu.RLock()
		defer r.snapshotMu.RUnlock()
		return r.retired
	})
	if !f.service.StorageBusy() {
		t.Error("storage became reclaimable before the callback/Core joined")
	}
	if _, err := f.service.Snapshot(f.ctx, r.id); !errors.Is(err, core.ErrClosed) {
		t.Errorf("retiring snapshot: %v", err)
	}
	if view, err := f.service.Summary(f.ctx, r.id); err != nil || view.ID != r.id {
		t.Errorf("directory did not use committed state: %+v %v", view, err)
	}
	if state := r.core.Load().State(); state.SessionID != r.id {
		t.Error("Close cleared the window before the admitted callback finished")
	}
	r.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.service.StorageBusy() {
		t.Fatal("fully closed runtime remains busy")
	}
	if _, err := f.service.Summary(f.ctx, r.id); err != nil {
		t.Fatal("retained closed runtime broke directory fallback:", err)
	}
	assertClosedRuntimeReaders(t, f, r.id)
}

type runtimeReopenGateStore struct {
	core.Store
	entered chan struct{}
	release chan struct{}
	fail    error
	held    atomic.Bool
}

func (s *runtimeReopenGateStore) LoadState(ctx context.Context, id string) (core.RuntimeState, error) {
	if s.held.CompareAndSwap(false, true) {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return core.RuntimeState{}, ctx.Err()
		}
		if s.fail != nil {
			return core.RuntimeState{}, s.fail
		}
	}
	return s.Store.LoadState(ctx, id)
}

func TestRuntimeBindingReopenKeepsClosedHandleUnreadable(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "published"
		if fail {
			name = "open-failed"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
				t.Error("binding reopen called the model")
				return nil
			})
			r, err := f.service.open(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			old := r.core.Load()
			gate := &runtimeReopenGateStore{Store: f.coreStore, entered: make(chan struct{}), release: make(chan struct{})}
			if fail {
				gate.fail = errors.New("binding reopen unavailable")
			}
			f.service.CoreStore = gate
			record := f.session
			record.BindingID = "new-binding" // changes the immutable compaction policy
			done := make(chan error, 1)
			go func() {
				r.mu.Lock()
				defer r.mu.Unlock()
				done <- r.syncBindingConfig(f.ctx, record)
			}()
			select {
			case <-gate.entered:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			if old.State().SessionID != "" {
				t.Error("fixture did not reach the gap after Core.Close")
			}
			if !f.service.StorageBusy() {
				t.Error("storage ignored an in-progress binding replacement")
			}
			if _, err := f.service.Snapshot(f.ctx, r.id); !errors.Is(err, core.ErrClosed) {
				t.Errorf("snapshot exposed a closed binding handle: %v", err)
			}
			if _, err := f.service.Summary(f.ctx, r.id); err != nil {
				t.Error("summary failed to read committed state during rebind:", err)
			}
			close(gate.release)
			err = <-done
			if fail {
				if !errors.Is(err, gate.fail) {
					t.Fatalf("lost open failure: %v", err)
				}
				if f.service.StorageBusy() {
					t.Error("failed reopen left completed Core close busy")
				}
				assertClosedRuntimeReaders(t, f, r.id)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if snapshot, err := f.service.Snapshot(f.ctx, r.id); err != nil || snapshot.State.SessionID != r.id || r.core.Load() == old {
					t.Fatalf("replacement was not safely published: %+v %v", snapshot, err)
				}
			}
		})
	}
}

func assertClosedRuntimeReaders(t *testing.T, f *fixture, id string) {
	t.Helper()
	if _, err := f.service.Core(f.ctx, id); !errors.Is(err, core.ErrClosed) {
		t.Errorf("Core returned a closed handle: %v", err)
	}
	if _, err := f.service.View(f.ctx, id); !errors.Is(err, core.ErrClosed) {
		t.Errorf("View did not reject closed handle: %v", err)
	}
	if err := f.service.WithSession(f.ctx, id, func(*core.Session) error {
		t.Error("WithSession invoked a callback with a closed handle")
		return nil
	}); !errors.Is(err, core.ErrClosed) {
		t.Errorf("WithSession returned %v", err)
	}
	if err := f.service.Compact(f.ctx, id, ""); !errors.Is(err, core.ErrClosed) {
		t.Errorf("Compact reached configuration on a closed handle: %v", err)
	}
}

func TestDeletingSubagentRetryRetainsDirectParentAuthorization(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Done.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "delete-retry", "Complete the task.", false)
	subagentTestFinished(t, f, child.SessionID)
	f.idle(t, parent.id)
	subagentRecoveryStopWorker(f)
	f.service.CoreStore = &subagentDeleteFaultStore{Store: f.coreStore, SessionDeleter: f.coreStore, branchDeleter: f.coreStore, target: child.SessionID}
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "first-delete", AgentControlInput{AgentID: child.SessionID, Signal: "delete"}); err == nil {
		t.Fatal("delete interruption was not surfaced")
	}
	var retained domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &retained); err != nil || retained.Deletion == nil {
		t.Fatalf("deletion intent missing: %+v %v", retained, err)
	}
	for _, input := range []AgentControlInput{
		{AgentID: child.SessionID},
		{AgentID: child.SessionID, Signal: "stop"},
		{AgentID: child.SessionID, Input: "new task"},
		{AgentID: child.SessionID, Model: f.agent.DefaultModelID},
	} {
		if _, err := f.service.controlSubagent(f.ctx, parent.id, "other-control", input); err == nil {
			t.Fatalf("non-delete control admitted a deleting subagent: %+v", input)
		}
	}
	if _, err := f.service.controlSubagent(f.ctx, "another-parent", "wrong-delete", AgentControlInput{AgentID: child.SessionID, Signal: "delete"}); err == nil {
		t.Fatal("another parent resumed the deletion")
	}
	f.service.CoreStore = f.coreStore
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "retry-delete", AgentControlInput{AgentID: child.SessionID, Signal: "delete"}); err != nil {
		t.Fatal("direct parent's delete retry could not complete:", err)
	}
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &retained); !domain.IsMissing(err) {
		t.Fatal("delete retry retained the child:", err)
	}
}

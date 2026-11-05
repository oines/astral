package harness

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type subagentWorkerStore struct {
	core.Store
	childID      string
	childIDs     map[string]bool
	loadTarget   int32
	stateReads   atomic.Int32
	loaded       chan struct{}
	failScan     atomic.Bool
	scanFailures atomic.Int32
	failed       chan struct{}
	uncertain    atomic.Bool
	uncertainID  atomic.Value
	confirmed    atomic.Int32
}

func (s *subagentWorkerStore) LoadState(ctx context.Context, id string) (core.RuntimeState, error) {
	state, err := s.Store.LoadState(ctx, id)
	if id == s.childID || s.childIDs[id] {
		if s.stateReads.Add(1) == max(1, s.loadTarget) && s.loaded != nil {
			close(s.loaded)
		}
	}
	return state, err
}

func (s *subagentWorkerStore) Scan(ctx context.Context, rollout string, query core.Query) (core.Page, error) {
	if query.SessionID == s.childID && s.failScan.CompareAndSwap(true, false) {
		s.scanFailures.Add(1)
		close(s.failed)
		return core.Page{}, errors.New("fixture temporary child-history read failure")
	}
	return s.Store.Scan(ctx, rollout, query)
}

func (s *subagentWorkerStore) Commit(ctx context.Context, id string, commit core.Commit) (core.Receipt, error) {
	receipt, err := s.Store.Commit(ctx, id, commit)
	if err == nil && s.uncertain.Load() {
		for _, entry := range commit.Entries {
			if entry.Run != nil && entry.Run.Status == "completed" && s.uncertain.CompareAndSwap(true, false) {
				s.uncertainID.Store(commit.ID)
				return core.Receipt{}, errors.New("fixture lost response after durable terminal commit")
			}
		}
	}
	return receipt, err
}

func (s *subagentWorkerStore) LookupCommit(ctx context.Context, id, commitID string) (core.Receipt, error) {
	receipt, err := s.Store.LookupCommit(ctx, id, commitID)
	if expected, ok := s.uncertainID.Load().(string); err == nil && ok && expected == commitID {
		s.confirmed.Add(1)
	}
	return receipt, err
}

func subagentWorkerCheckpoint(t *testing.T, f *fixture, id string, state core.RuntimeState) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	eventually(t, ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(ctx, "subagent_delivery", id, &checkpoint) == nil && checkpoint.Version == state.Version
	})
}

func TestSubagentWorkerSleepsUntilDurableHostWorkChanges(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Completed work.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "event-driven-cold-child", "Finish the initial instruction.", false)
	state := subagentTestFinished(t, f, child.SessionID)
	subagentWorkerCheckpoint(t, f, child.SessionID, state)
	f.idle(t, parent.id)
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	counted := &subagentWorkerStore{Store: f.coreStore, childID: child.SessionID, loaded: make(chan struct{})}
	f.service = New(f.store, counted, f.service.Models, f.hub)
	select {
	case <-counted.loaded:
	case <-f.ctx.Done():
		t.Fatal("restart did not perform the initial cold-child sweep")
	}
	// Join the child's first reconciliation before observing the idle period.
	f.service.subagentMu.Lock()
	reads := counted.stateReads.Load()
	f.service.subagentMu.Unlock()
	requests := f.count()
	select {
	case <-time.After(1200 * time.Millisecond):
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if after := counted.stateReads.Load(); after != reads {
		t.Fatalf("settled cold child was polled without an event: state reads %d -> %d", reads, after)
	}
	f.service.mu.Lock()
	loadedRuntimes := len(f.service.runtimes)
	f.service.mu.Unlock()
	if loadedRuntimes != 0 || f.count() != requests {
		t.Fatal("idle recovery opened a cold runtime or generated a reply")
	}
	// These real Host writes do not change child admission or its source facts.
	// In particular, Owner IM resume must remain durable without a child sweep.
	if err := f.store.SaveMaintenance(f.ctx, "im_inbox_resume", "unrelated-binding", "complete", map[string]int{"Version": 1}); err != nil {
		t.Fatal(err)
	}
	var checkpoint subagentCheckpoint
	if err := f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveMaintenance(f.ctx, "subagent_delivery", child.SessionID, "complete", checkpoint); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"outbox", "auth_sessions"} {
		value := struct{ domain.Meta }{Meta: domain.Meta{ID: "unrelated-" + kind}}
		if err := f.store.Save(f.ctx, kind, &value.Meta, &value); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.SaveDelivery(f.ctx, persistence.Delivery{EventID: "unrelated-delivery", SessionID: parent.id, State: "routed"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if after := counted.stateReads.Load(); after != reads {
		t.Fatalf("unrelated Host writes reread settled child state: %d -> %d", reads, after)
	}
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Name += " changed"
	if err := f.store.Save(f.ctx, "models", &profile.Meta, &profile); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool { return counted.stateReads.Load() > reads })
	reads = counted.stateReads.Load()
	var provider domain.Provider
	if err := f.store.Get(f.ctx, "providers", profile.ProviderID, &provider); err != nil {
		t.Fatal(err)
	}
	provider.Name += " changed"
	if err := f.store.Save(f.ctx, "providers", &provider.Meta, &provider); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool { return counted.stateReads.Load() > reads })
	// A direct settings commit must synchronize the child without an explicit
	// Core wake or a new driving instruction.
	target := subagentTreeModel(t, f)
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Configuration.Selection = core.ModelSelection{ID: target.ID}
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		selection, err := f.service.ModelSelection(f.ctx, child.SessionID)
		return err == nil && selection.ID == target.ID
	})
	if f.count() != requests {
		t.Fatal("metadata synchronization started a new model request")
	}
	// Only a durable Host write wakes the worker here: no explicit wake call.
	activation := subagentActivation{Input: core.Input{ID: "event-driven-cold-activation", Message: core.User("Perform the next instruction.")}}
	if err := f.store.SaveMaintenance(f.ctx, "subagent_activation", child.SessionID, "running", activation); err != nil {
		t.Fatal(err)
	}
	subagentRecoveryReceipt(t, f, child.SessionID, activation.Input.ID)
	state = subagentTestFinished(t, f, child.SessionID)
	subagentWorkerCheckpoint(t, f, child.SessionID, state)
	var saved subagentActivation
	if err := f.store.Maintenance(f.ctx, "subagent_activation", child.SessionID, &saved); err != nil || !saved.Complete {
		t.Fatalf("Host commit did not activate cold work: complete=%v err=%v", saved.Complete, err)
	}
}

func TestSubagentWorkerRetriesFailedDeliveryWithoutAnotherCommit(t *testing.T) {
	testSubagentWorkerDeliveryRetry(t, false)
}

func TestSubagentWorkerFailureDeadlineSurvivesUnrelatedWakes(t *testing.T) {
	testSubagentWorkerDeliveryRetry(t, true)
}

func testSubagentWorkerDeliveryRetry(t *testing.T, unrelatedWakes bool) {
	t.Helper()
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		return textReply("Report after temporary storage failure.")
	})
	parent := subagentTestSeed(t, f)
	subagentRecoveryStopWorker(f)
	child := subagentTestSpawn(t, f, parent, "event-driven-retry", "Complete before the parent notification can be delivered.", false)
	runtime, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	err = runtime.startSubagentLocked(f.ctx)
	runtime.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	state := subagentTestFinished(t, f, child.SessionID)
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	counted := &subagentWorkerStore{Store: f.coreStore, childID: child.SessionID, failed: make(chan struct{})}
	counted.failScan.Store(true)
	f.service = New(f.store, counted, f.service.Models, f.hub)
	select {
	case <-counted.failed:
	case <-f.ctx.Done():
		t.Fatal("fixture did not reach the unacknowledged delivery read")
	}
	// No new Host/Core write is made. The error must schedule its own retry;
	// unrelated ID wakes must neither perform that work nor postpone its deadline.
	if unrelatedWakes {
		ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
		defer cancel()
		go func() {
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					f.service.wakeSubagents("unrelated-root")
				}
			}
		}()
		eventually(t, ctx, func() bool {
			var checkpoint subagentCheckpoint
			return f.store.Maintenance(ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
		})
	} else {
		subagentWorkerCheckpoint(t, f, child.SessionID, state)
	}
	f.idle(t, parent.id)
	if counted.scanFailures.Load() != 1 {
		t.Fatal("temporary read fault was not exercised exactly once")
	}
	terminal := 0
	for _, entry := range subagentTestEntries(t, f, parent.id) {
		if entry.Message != nil && entry.Message.ContextKind == subagentUpdateKind && strings.Contains(string(encode(entry.Message)), "naturally ended Run "+state.LastRun.ID) {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("retry delivered %d terminal notifications, want exactly one", terminal)
	}
}

func TestSubagentWorkerObservesConfirmedUncertainTerminalCommit(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Durable child result.") })
	parent := subagentTestSeed(t, f)
	counted := &subagentWorkerStore{Store: f.coreStore}
	counted.uncertain.Store(true)
	// Host inspection and the newly opened child's Core share the fault Store;
	// the already-open parent's Core remains on its original Store.
	f.service.subagentMu.Lock()
	f.service.CoreStore = counted
	f.service.subagentMu.Unlock()
	child := subagentTestSpawn(t, f, parent, "event-driven-uncertain", "Complete with a lost commit response.", false)
	state := subagentTestFinished(t, f, child.SessionID)
	subagentWorkerCheckpoint(t, f, child.SessionID, state)
	f.idle(t, parent.id)
	if counted.confirmed.Load() != 1 || counted.uncertain.Load() {
		t.Fatalf("Core did not confirm the uncertain terminal commit: confirmations=%d pending=%v", counted.confirmed.Load(), counted.uncertain.Load())
	}
	if runs := subagentTerminalRunIDs(t, f, child.SessionID, "completed"); len(runs) != 1 || runs[0] != state.LastRun.ID {
		t.Fatalf("uncertain acknowledgement duplicated execution: %v", runs)
	}
	if f.service.CoreStore != counted {
		t.Fatal("Core configuration replaced the Host-owned Store")
	}
}

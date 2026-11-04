package harness

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

type coldQueryStore struct {
	core.Store
	pathReads    atomic.Int64
	stateReads   atomic.Int64
	nodeReads    atomic.Int64
	pendingReads atomic.Int64
}

func (s *coldQueryStore) LoadState(ctx context.Context, id string) (core.RuntimeState, error) {
	s.stateReads.Add(1)
	return s.Store.LoadState(ctx, id)
}

func (s *coldQueryStore) Get(ctx context.Context, rollout, id string) (core.Record, error) {
	s.nodeReads.Add(1)
	return s.Store.Get(ctx, rollout, id)
}

func (s *coldQueryStore) Pending(ctx context.Context, id string, query core.PendingQuery) (core.Page, error) {
	s.pendingReads.Add(1)
	return s.Store.Pending(ctx, id, query)
}

func (s *coldQueryStore) resetReads() {
	s.stateReads.Store(0)
	s.nodeReads.Store(0)
	s.pathReads.Store(0)
	s.pendingReads.Store(0)
}

func TestSummaryKeepsHistoryColdAndUsesMemoryForHotSessions(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Finished.") })
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "summary-input", Text: "Complete this request."}); err != nil {
		t.Fatal(err)
	}
	before := f.idle(t, f.session.ID)
	f.restart(t)
	subagentRecoveryStopWorker(f)
	counted := &coldQueryStore{Store: f.coreStore}
	f.service.CoreStore = counted
	for range 3 {
		summary, err := f.service.Summary(f.ctx, f.session.ID)
		if err != nil || summary.Selection.ID != before.Selection.ID || summary.UpdatedAt != before.UpdatedAt {
			t.Fatalf("cold summary changed selection or activity: %+v err=%v", summary, err)
		}
	}
	if counted.stateReads.Load() != 3 || counted.nodeReads.Load() != 4 || counted.pathReads.Load() != 0 || len(f.service.runtimes) != 0 {
		t.Fatalf("cold summary must read one state/config per query and the unchanged head only once: states=%d nodes=%d paths=%d runtimes=%d", counted.stateReads.Load(), counted.nodeReads.Load(), counted.pathReads.Load(), len(f.service.runtimes))
	}
	t.Logf("COLD_SUMMARY repeats=3 state_reads=%d node_reads=%d history_paths=%d loaded_runtimes=%d", counted.stateReads.Load(), counted.nodeReads.Load(), counted.pathReads.Load(), len(f.service.runtimes))
	if _, err := f.service.View(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	counted.resetReads()
	for range 3 {
		if _, err := f.service.Summary(f.ctx, f.session.ID); err != nil {
			t.Fatal(err)
		}
	}
	if counted.stateReads.Load() != 0 || counted.nodeReads.Load() != 0 || counted.pathReads.Load() != 0 {
		t.Fatalf("hot summary reread durable state, configuration or unchanged head: states=%d nodes=%d paths=%d", counted.stateReads.Load(), counted.nodeReads.Load(), counted.pathReads.Load())
	}
	t.Log("HOT_SUMMARY repeats=3 state_reads=0 node_reads=0 history_paths=0")
	session := f.service.runtimes[f.session.ID].core.Load()
	previousHead := session.State().Branch.HeadID
	if _, err := session.InjectContext(f.ctx, core.Input{ID: "new-directory-activity", Contexts: []core.Message{core.User("A newly applied passive observation.")}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		snapshot := session.Snapshot()
		return !snapshot.Maintenance && snapshot.State.Branch.HeadID != previousHead
	})
	counted.resetReads()
	changed, err := f.service.Summary(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	head := session.State().Branch.HeadID
	record, err := f.coreStore.Get(f.ctx, session.State().RolloutID, head)
	if err != nil || changed.UpdatedAt != record.Entry.Time || counted.stateReads.Load() != 0 || counted.nodeReads.Load() != 1 {
		t.Fatalf("changed head did not refresh exactly its timestamp: summary=%+v state_reads=%d node_reads=%d err=%v", changed, counted.stateReads.Load(), counted.nodeReads.Load(), err)
	}
	// Exercise the lifecycle cleanup used by permanent deletion without hiding
	// the Store's optional deletion capability behind the read-only spy.
	if err := f.service.closeDeletedRuntime(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	f.service.removeDeletedRuntime(f.session.ID)
	if _, retained := f.service.headTimes[f.session.ID]; retained {
		t.Fatal("deleted Session retained a head timestamp memo")
	}
}

func TestDirectoryDoesNotDecodeQueuedInputBodies(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Not expected.") })
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Steer(f.ctx, core.Input{ID: "large-queued-input", Message: core.User(strings.Repeat("Queued owner text. ", 8192))}); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	subagentRecoveryStopWorker(f)
	counted := &coldQueryStore{Store: f.coreStore}
	f.service.CoreStore = counted
	for range 3 {
		if _, err := f.service.Summary(f.ctx, f.session.ID); err != nil {
			t.Fatal(err)
		}
	}
	if counted.pendingReads.Load() != 0 || counted.pathReads.Load() != 0 || len(f.service.runtimes) != 0 || f.count() != 0 {
		t.Fatalf("directory decoded pending bodies or loaded/executed the Session: pending=%d paths=%d runtimes=%d model_calls=%d", counted.pendingReads.Load(), counted.pathReads.Load(), len(f.service.runtimes), f.count())
	}
	view, err := f.service.View(f.ctx, f.session.ID)
	if err != nil || len(view.Pending) != 1 || counted.pendingReads.Load() == 0 {
		t.Fatalf("explicit full view lost queued input detail: pending=%+v reads=%d err=%v", view.Pending, counted.pendingReads.Load(), err)
	}
	t.Log("COLD_PENDING_DIRECTORY repeats=3 decoded_pending_pages=0 history_paths=0 loaded_runtimes=0")
}

func (s *coldQueryStore) Scan(ctx context.Context, rollout string, query core.Query) (core.Page, error) {
	if query.PathHead != "" {
		s.pathReads.Add(1)
	}
	return s.Store.Scan(ctx, rollout, query)
}

func TestDirectoryAndReceiptQueriesDoNotOpenCoreSession(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Finished.") })
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "cold-query-input", Text: "Keep the completed receipt."}); err != nil {
		t.Fatal(err)
	}
	before := f.idle(t, f.session.ID)
	f.restart(t)
	subagentRecoveryStopWorker(f)
	counted := &coldQueryStore{Store: f.coreStore}
	f.service.CoreStore = counted
	state, err := f.coreStore.LoadState(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		view, err := f.service.Summary(f.ctx, f.session.ID)
		if err != nil || view.Status != before.Status || view.ProfileID != before.ProfileID || view.UpdatedAt != before.UpdatedAt || !view.ModelAvailable {
			t.Fatalf("directory changed the completed Session: %+v err=%v", view, err)
		}
		raw, _ := json.Marshal(view)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		for _, field := range []string{"can_continue", "pending", "approvals", "unresolved", "context_usage"} {
			if _, exists := fields[field]; exists {
				t.Fatalf("directory pretended to return execution detail %s", field)
			}
		}
		receipt, err := f.service.Receipt(f.ctx, f.session.ID, "cold-query-input")
		if err != nil || receipt.SessionID != f.session.ID || receipt.State != string(core.InputApplied) {
			t.Fatalf("cold receipt differs: %+v err=%v", receipt, err)
		}
		if err = f.service.ResumeContext(f.ctx, f.session.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.service.runtimes) != 0 || counted.pathReads.Load() != 0 {
		t.Fatalf("directory/receipt/empty recovery loaded Core: runtimes=%d paths=%d", len(f.service.runtimes), counted.pathReads.Load())
	}
	after, err := f.coreStore.LoadState(f.ctx, f.session.ID)
	if err != nil || state.Version != after.Version {
		t.Fatalf("read-only queries changed durable execution state: %d -> %d err=%v", state.Version, after.Version, err)
	}
}

func TestSettledSubagentRecoveryStaysColdButNewInstructionActivates(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Finished.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "cold-child", "Finish the initial task.", false)
	state := subagentTestFinished(t, f, child.SessionID)
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	f.idle(t, parent.id)
	f.restart(t)
	subagentRecoveryStopWorker(f)
	counted := &coldQueryStore{Store: f.coreStore}
	f.service.CoreStore = counted
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.service.runtimes) != 0 || counted.stateReads.Load() != 3 || counted.nodeReads.Load() != 3 || counted.pathReads.Load() != 0 {
		t.Fatalf("cold settled recovery should read metadata, not execution windows: runtimes=%d states=%d nodes=%d paths=%d", len(f.service.runtimes), counted.stateReads.Load(), counted.nodeReads.Load(), counted.pathReads.Load())
	}
	t.Log("COLD_CHILD repeats=3 state_reads=3 configuration_reads=3 history_paths=0 loaded_runtimes=0")
	activation := subagentActivation{Input: core.Input{ID: "cold-child-new-instruction", Message: core.User("Now do a different task.")}}
	if err := f.store.SaveMaintenance(f.ctx, "subagent_activation", child.SessionID, "running", activation); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	subagentRecoveryReceipt(t, f, child.SessionID, activation.Input.ID)
	f.idle(t, child.SessionID)
	var saved subagentActivation
	if err := f.store.Maintenance(f.ctx, "subagent_activation", child.SessionID, &saved); err != nil || !saved.Complete {
		t.Fatalf("new instruction was not recovered: %+v err=%v", saved, err)
	}
}

func TestHotSubagentPollingUsesItsLoadedState(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Finished.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "hot-child", "Finish.", false)
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
	counted := &coldQueryStore{Store: f.coreStore}
	f.service.CoreStore = counted
	for range 3 {
		if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if counted.stateReads.Load() != 0 || counted.nodeReads.Load() != 0 || counted.pathReads.Load() != 0 {
		t.Fatalf("hot child polling reread durable metadata: states=%d nodes=%d paths=%d", counted.stateReads.Load(), counted.nodeReads.Load(), counted.pathReads.Load())
	}
	t.Log("HOT_CHILD repeats=3 state_reads=0 configuration_reads=0 history_paths=0")
}

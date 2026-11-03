package harness

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

// Lose both acknowledgements after one real configuration commit. The live
// Session cannot know that its selection changed, but reopening can read it.
type subagentModelUncertainCommitStore struct {
	core.Store
	sessionID string
	selection core.ModelSelection
	commitErr error
	lookupErr error
	mu        sync.Mutex
	commitID  string
	lookups   int
}

func (s *subagentModelUncertainCommitStore) Commit(ctx context.Context, id string, commit core.Commit) (core.Receipt, error) {
	receipt, err := s.Store.Commit(ctx, id, commit)
	if err != nil || id != s.sessionID {
		return receipt, err
	}
	for _, entry := range commit.Entries {
		if entry.Configuration == nil || !reflect.DeepEqual(entry.Configuration.Selection, s.selection) {
			continue
		}
		s.mu.Lock()
		if s.commitID == "" {
			s.commitID = commit.ID
			s.mu.Unlock()
			return core.Receipt{}, s.commitErr
		}
		s.mu.Unlock()
	}
	return receipt, nil
}

func (s *subagentModelUncertainCommitStore) LookupCommit(ctx context.Context, id, key string) (core.Receipt, error) {
	s.mu.Lock()
	failed := id == s.sessionID && key == s.commitID && s.commitID != ""
	if failed {
		s.lookups++
	}
	s.mu.Unlock()
	if failed {
		return core.Receipt{}, s.lookupErr
	}
	return s.Store.LookupCommit(ctx, id, key)
}

func TestSubagentModelChangeRetainsIntentAfterCommittedWriteLosesBothAcknowledgements(t *testing.T) {
	var childWire atomic.Value
	childWire.Store("")
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "You are a subagent of Session") {
			model, _ := request["model"].(string)
			reasoning, _ := request["reasoning_effort"].(string)
			childWire.Store(model + "|" + reasoning)
		}
		return textReply("Recovered model selection.")
	})
	target := subagentTreeModel(t, f)
	wanted := core.ModelSelection{ID: target.ID, Reasoning: &core.ReasoningOptions{Effort: "high"}}
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	fault := &subagentModelUncertainCommitStore{
		Store: f.coreStore, sessionID: f.session.ID, selection: wanted,
		commitErr: errors.New("model selection committed without acknowledgement"),
		lookupErr: errors.New("model selection receipt temporarily unavailable"),
	}
	f.service = New(f.store, fault, f.service.Models, f.hub)
	subagentRecoveryStopWorker(f)
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "uncertain-model-child", "Use the recovered selection on the first request.", false)
	original := parent.core.Load().Configuration().Selection
	if err := f.service.SelectModel(f.ctx, parent.id, wanted.ID, wanted.Reasoning); !errors.Is(err, fault.commitErr) {
		t.Fatalf("expected uncertain committed write: %v", err)
	}
	fault.mu.Lock()
	commitID, lookups := fault.commitID, fault.lookups
	fault.mu.Unlock()
	if commitID == "" || lookups != 1 {
		t.Fatalf("fault did not cover the committed write and receipt recovery: commit=%q lookups=%d", commitID, lookups)
	}
	if receipt, err := f.coreStore.LookupCommit(f.ctx, parent.id, commitID); err != nil || receipt.CommitID != commitID {
		t.Fatalf("injected write was not really committed: %+v %v", receipt, err)
	}
	durable, err := core.ReadConfiguration(f.ctx, f.coreStore, parent.id)
	if err != nil || !reflect.DeepEqual(durable.Selection, wanted) {
		t.Fatalf("root did not durably switch before losing acknowledgement: %+v %v", durable.Selection, err)
	}
	if got := parent.core.Load().Configuration().Selection; !reflect.DeepEqual(got, original) {
		t.Fatalf("fault did not leave the live root uncertain: before=%+v after=%+v", original, got)
	}
	var pending subagentModelChange
	if err := f.store.Maintenance(f.ctx, subagentModelChangeKind, parent.id, &pending); err != nil || !reflect.DeepEqual(pending.Selection, wanted) {
		t.Fatalf("uncertain write lost its recovery intent: %+v %v", pending, err)
	}
	var before subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &before); err != nil || !reflect.DeepEqual(before.Configuration.Selection, original) {
		t.Fatalf("fixture already propagated child settings: %+v %v", before.Configuration.Selection, err)
	}
	f.restart(t)
	subagentWaitTreeSelection(t, f, []string{parent.id, child.SessionID}, wanted)
	subagentTestFinished(t, f, child.SessionID)
	if wire := childWire.Load().(string); wire != target.ModelID+"|high" {
		t.Fatalf("recovered child's first request used stale model/reasoning: %q", wire)
	}
	eventually(t, f.ctx, func() bool {
		return domain.IsMissing(f.store.Maintenance(f.ctx, subagentModelChangeKind, parent.id, &pending))
	})
}

func TestRootSpawnReconcilesPendingModelChangeBeforeInheritingSelection(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Inherited current model selection.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "pending-model-parent", "Create a descendant later.", false)
	state := subagentTestFinished(t, f, child.SessionID)
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	f.idle(t, parent.id)
	subagentRecoveryStopWorker(f)
	childRuntime, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	original := childRuntime.core.Load().Configuration().Selection
	target := subagentTreeModel(t, f)
	wanted := core.ModelSelection{ID: target.ID, Reasoning: &core.ReasoningOptions{Effort: "high"}}
	change := subagentModelChange{RootID: parent.id, Selection: wanted, SessionIDs: []string{parent.id, child.SessionID}}
	if err := f.store.SaveMaintenance(f.ctx, subagentModelChangeKind, parent.id, "running", change); err != nil {
		t.Fatal(err)
	}
	// Stop exactly between the root's Core commit and the Host fan-out. No
	// worker can reconcile it before the root creates another direct child.
	if err := parent.core.Load().SelectModel(f.ctx, wanted); err != nil {
		t.Fatal(err)
	}
	var before subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &before); err != nil || !reflect.DeepEqual(before.Configuration.Selection, original) {
		t.Fatalf("fixture already propagated child settings: %+v %v", before.Configuration.Selection, err)
	}
	newChild := subagentTestSpawn(t, f, parent, "spawn-before-model-worker", "Inherit the latest model and reasoning.", false)
	for _, id := range []string{child.SessionID, newChild.SessionID} {
		var settings subagentSettings
		if err := f.store.Get(f.ctx, "subagent_settings", id, &settings); err != nil || !reflect.DeepEqual(settings.Configuration.Selection, wanted) {
			t.Fatalf("spawn inherited stale settings for %s: %+v %v", id, settings.Configuration.Selection, err)
		}
	}
	var pending subagentModelChange
	if err := f.store.Maintenance(f.ctx, subagentModelChangeKind, parent.id, &pending); !domain.IsMissing(err) {
		t.Fatalf("spawn did not acknowledge the pending model change: %+v %v", pending, err)
	}
	if got := childRuntime.core.Load().Configuration().Selection; !reflect.DeepEqual(got, original) {
		t.Fatalf("fixture no longer exercises an old live child selection: %+v", got)
	}
	if _, err := f.coreStore.LoadState(f.ctx, newChild.SessionID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("spawn initialized the queued child before worker recovery: %v", err)
	}
	f.restart(t)
	subagentWaitTreeSelection(t, f, []string{parent.id, child.SessionID, newChild.SessionID}, wanted)
	subagentTestFinished(t, f, newChild.SessionID)
}

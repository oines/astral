package harness

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

type coldQueryStore struct {
	core.Store
	pathReads atomic.Int64
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
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.service.runtimes) != 0 {
		t.Fatalf("settled recovery opened %d execution instances", len(f.service.runtimes))
	}
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

type hostPreparationModel struct {
	core.Model
	invalid bool
}

func (m hostPreparationModel) PrepareRequest(_ context.Context, request core.Request) (core.Request, error) {
	request.Messages[0].Content[0].Text = "Prepared."
	if m.invalid {
		request.ModelID = "different-model"
	}
	return request, nil
}

func TestSessionPreparationKeepsCoreOwnershipAndConfigurationValidation(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		request := core.Request{ModelID: "original-model", SystemPrompt: "test", Messages: []core.Message{core.User("Original.")}, MaxOutputTokens: 32}
		model := sessionModel{Model: hostPreparationModel{invalid: invalid}, runtime: &runtime{subagent: &subagentSettings{}}}
		prepared, err := core.PrepareRequest(context.Background(), model, request)
		if invalid && err == nil || !invalid && err != nil {
			t.Fatalf("configuration validation changed: invalid=%v err=%v", invalid, err)
		}
		if request.Messages[0].Content[0].Text != "Original." {
			t.Fatal("delegated preparation changed the caller's request")
		}
		if !invalid && prepared.Messages[0].Content[0].Text != "Prepared." {
			t.Fatal("Host did not delegate preparation")
		}
	}
}

package harness

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/domain"
)

type subagentFailureSyncCommitFault struct {
	core.Store
	sessionID string
	modelID   string
	err       error
	calls     atomic.Int32
}

func (s *subagentFailureSyncCommitFault) Commit(ctx context.Context, id string, commit core.Commit) (core.Receipt, error) {
	if id == s.sessionID {
		for _, entry := range commit.Entries {
			if entry.Configuration != nil && entry.Configuration.Selection.ID == s.modelID {
				s.calls.Add(1)
				return core.Receipt{}, s.err
			}
		}
	}
	return s.Store.Commit(ctx, id, commit)
}

func TestSubagentPersistedFailureIsDeliveredBeforeUnavailableDesiredModelSync(t *testing.T) {
	var parentRequests atomic.Int32
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "failed Run") {
			parentRequests.Add(1)
		}
		return textReply("Parent reviewed the persisted failure independently of model synchronization.")
	})
	dispatches := subagentFailureInstall401(f)
	parent := subagentTestSeed(t, f)
	subagentRecoveryStopWorker(f)
	child := subagentTestSpawn(t, f, parent, "failed-before-desired-sync", "Produce a real provider failure before the desired selection changes.", false)
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
	failed := subagentFailureWait(t, f, child.SessionID)
	if dispatches.Load() != 1 || len(subagentFailureNotifications(t, f, parent.id, child.SessionID, failed.LastRun.ID)) != 0 {
		t.Fatal("fixture did not stop after the real child failure and before parent delivery")
	}
	model := anotherModel(t, f)
	disabled := false
	model.Enabled = &disabled
	if err := f.service.Models.SaveProfile(f.ctx, &model); err != nil {
		t.Fatal(err)
	}
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Configuration.Selection = core.ModelSelection{ID: model.ID}
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
		t.Fatal(err)
	}
	// Reopen both durable stores before delivery. SelectModel itself does not
	// resolve provider availability, so also fail this child's configuration
	// commit to prove that an actual sync error cannot hide the earlier Run.
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.coreStore.Close(); err != nil {
		t.Fatal(err)
	}
	f.coreStore, err = sqlite.Open(filepath.Join(f.directory, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	fault := &subagentFailureSyncCommitFault{Store: f.coreStore, sessionID: child.SessionID, modelID: model.ID, err: errors.New("desired child configuration commit failed")}
	f.service = New(f.store, fault, f.service.Models, f.hub)
	subagentRecoveryStopWorker(f)
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileSubagent(f.ctx, record); !errors.Is(err, fault.err) || fault.calls.Load() == 0 {
		t.Fatalf("fixture did not exercise a failing desired-model sync: calls=%d error=%v", fault.calls.Load(), err)
	}
	notification := subagentFailureAssertNotification(t, f, parent.id, child.SessionID, failed)
	subagentRecoveryReceipt(t, f, parent.id, "subagent_run_end_"+child.SessionID+"_"+failed.LastRun.ID)
	f.idle(t, parent.id)
	for range 3 {
		if err := f.service.reconcileSubagent(f.ctx, record); !errors.Is(err, fault.err) {
			t.Fatalf("sync fault disappeared before replay coverage: %v", err)
		}
	}
	if notifications := subagentFailureNotifications(t, f, parent.id, child.SessionID, failed.LastRun.ID); len(notifications) != 1 || notifications[0].ID != notification.ID || parentRequests.Load() != 1 {
		t.Fatalf("retrying failed model synchronization replayed parent notification: notifications=%d requests=%d", len(notifications), parentRequests.Load())
	}
	f.restart(t)
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	f.idle(t, parent.id)
	if notifications := subagentFailureNotifications(t, f, parent.id, child.SessionID, failed.LastRun.ID); len(notifications) != 1 || notifications[0].ID != notification.ID || parentRequests.Load() != 1 || dispatches.Load() != 1 {
		t.Fatalf("restart duplicated notification or reran the failed child: notifications=%d parent requests=%d child requests=%d", len(notifications), parentRequests.Load(), dispatches.Load())
	}
}

func TestSubagentPendingOlderReportsDoNotBlockControlActivationRecovery(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	var holding atomic.Bool
	var resumedChild atomic.Int32
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		raw := string(encode(request))
		if strings.Contains(raw, "PARENT_BLOCKED_DURING_CHILD_ACTIVATION") && !holding.Swap(true) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
		}
		if strings.Contains(raw, "You are a subagent of Session") && strings.Contains(raw, "RECOVER_CHILD_WHILE_OLD_REPORT_PENDING") {
			resumedChild.Add(1)
		}
		return textReply("A report preceding a later control activation.")
	})
	t.Cleanup(func() { released.Do(func() { close(release) }) })
	parent := subagentTestSeed(t, f)
	subagentRecoveryStopWorker(f)
	child := subagentTestSpawn(t, f, parent, "pending-report-activation", "Finish the first assignment before the parent gets its report.", false)
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
	before := subagentTestFinished(t, f, child.SessionID)
	f.idle(t, child.SessionID)
	if _, err := f.service.Submit(f.ctx, parent.id, InputRequest{ID: "parent-blocked-activation", Text: "PARENT_BLOCKED_DURING_CHILD_ACTIVATION"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	control := AgentControlInput{AgentID: child.SessionID, Input: "RECOVER_CHILD_WHILE_OLD_REPORT_PENDING"}
	message := core.User(control.Input)
	message.ContextKind = "astral.subagent_instruction"
	message.Metadata = encode(map[string]string{"source": "parent", "parent_session_id": parent.id})
	activation := subagentActivation{Input: core.Input{ID: "subagent_instruction_pending-report-recovery", Digest: digest(control), Message: message}}
	if err := f.store.SaveMaintenance(f.ctx, "subagent_activation", child.SessionID, "running", activation); err != nil {
		t.Fatal(err)
	}
	if receipt, err := r.core.Load().Steer(f.ctx, activation.Input); err != nil || receipt.State != core.InputPending {
		t.Fatalf("persist activation before simulated Continue crash: %+v %v", receipt, err)
	}
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileSubagent(f.ctx, record); !errors.Is(err, errSubagentReportsPending) {
		t.Fatalf("fixture did not keep the older terminal behind pending parent reports: %v", err)
	}
	eventually(t, f.ctx, func() bool { return resumedChild.Load() == 1 })
	subagentRecoveryReceipt(t, f, child.SessionID, activation.Input.ID)
	if receipt, err := f.coreStore.LookupInput(f.ctx, parent.id, "subagent_run_end_"+child.SessionID+"_"+before.LastRun.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("old terminal crossed pending report admission: %+v %v", receipt, err)
	}
	if !parent.core.Load().Snapshot().Running {
		t.Fatal("parent was not still blocked while the child's control activation recovered")
	}
	var recovered subagentActivation
	if err := f.store.Maintenance(f.ctx, "subagent_activation", child.SessionID, &recovered); err != nil || !recovered.Complete {
		t.Fatalf("pending old reports blocked the durable activation acknowledgement: %+v %v", recovered, err)
	}
	released.Do(func() { close(release) })
	f.idle(t, parent.id)
}

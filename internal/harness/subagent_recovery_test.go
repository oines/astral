package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func subagentRecoveryStopWorker(f *fixture) {
	// Runtime lifetimes are owned by Core. Cancel only the Host worker so the
	// test can persist admission and acknowledgement without racing activation.
	f.service.cancel()
	f.service.subagentWorkers.Wait()
}

func subagentRecoveryReceipt(t *testing.T, f *fixture, sessionID, inputID string) core.InputReceipt {
	t.Helper()
	var receipt core.InputReceipt
	eventually(t, f.ctx, func() bool {
		var err error
		receipt, err = f.coreStore.LookupInput(f.ctx, sessionID, inputID)
		return err == nil && receipt.State == core.InputApplied
	})
	return receipt
}

func TestSubagentInitialAdmissionResumesAfterStartedAcknowledgementCrash(t *testing.T) {
	var childCalls atomic.Int32
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "You are a subagent of Session") {
			childCalls.Add(1)
		}
		return textReply("Recovered initial result.")
	})
	parent := subagentTestSeed(t, f)
	subagentRecoveryStopWorker(f)
	task := "INITIAL_ADMISSION_RECOVERY_MARKER"
	child := subagentTestSpawn(t, f, parent, "initial-crash", task, false)
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	message := core.User(task)
	message.ContextKind = "astral.subagent_instruction"
	input := core.Input{ID: "subagent_initial_" + child.SessionID, Digest: digest(task), Message: message}
	receipt, err := r.core.Load().Steer(f.ctx, input)
	if err != nil || receipt.State != core.InputPending {
		t.Fatalf("persist initial admission: %+v %v", receipt, err)
	}
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Started = true
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
		t.Fatal(err)
	}
	if snapshot := r.core.Load().Snapshot(); snapshot.Running || snapshot.State.LastRun != nil || childCalls.Load() != 0 {
		t.Fatalf("fixture crossed Continue before the simulated crash: %+v", snapshot)
	}
	f.restart(t)
	subagentRecoveryReceipt(t, f, child.SessionID, input.ID)
	state := subagentTestFinished(t, f, child.SessionID)
	if childCalls.Load() != 1 {
		t.Fatalf("initial recovery generated %d times, want 1", childCalls.Load())
	}
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	f.restart(t)
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	// Explicitly reconcile as well: an already-acknowledged Started flag and
	// applied input must never create a second initial Run after another reopen.
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	if childCalls.Load() != 1 {
		t.Fatal("reopening an applied initial input started duplicate work")
	}
	accepted := 0
	for _, entry := range subagentTestEntries(t, f, child.SessionID) {
		if entry.Message != nil && entry.Message.ContextKind == "astral.subagent_instruction" && strings.Contains(string(encode(entry.Message)), task) {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("initial instruction appears %d times in child history, want 1", accepted)
	}
}

func TestSubagentControlAdmissionResumesFromActivationJournalExactlyOnce(t *testing.T) {
	var childCalls atomic.Int32
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "You are a subagent of Session") {
			childCalls.Add(1)
		}
		return textReply("Recovered control result.")
	})
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "control-recovery", "Initial work before the crash.", false)
	state := subagentTestFinished(t, f, child.SessionID)
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	subagentRecoveryStopWorker(f)
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	control := AgentControlInput{AgentID: child.SessionID, Input: "FOLLOWUP_ADMISSION_RECOVERY_MARKER"}
	message := core.User(control.Input)
	message.ContextKind = "astral.subagent_instruction"
	message.Metadata = encode(map[string]string{"source": "parent", "parent_session_id": parent.id})
	activation := subagentActivation{Input: core.Input{ID: "subagent_instruction_control-crash", Digest: digest(control), Message: message}}
	if err := f.store.SaveMaintenance(f.ctx, "subagent_activation", child.SessionID, "running", activation); err != nil {
		t.Fatal(err)
	}
	receipt, err := r.core.Load().Steer(f.ctx, activation.Input)
	if err != nil || receipt.State != core.InputPending || r.core.Load().Snapshot().Running || childCalls.Load() != 1 {
		t.Fatalf("fixture did not stop between control admission and Continue: receipt=%+v calls=%d error=%v", receipt, childCalls.Load(), err)
	}
	f.restart(t)
	subagentRecoveryReceipt(t, f, child.SessionID, activation.Input.ID)
	subagentTestFinished(t, f, child.SessionID)
	eventually(t, f.ctx, func() bool {
		var recovered subagentActivation
		return f.store.Maintenance(f.ctx, "subagent_activation", child.SessionID, &recovered) == nil && recovered.Complete
	})
	if childCalls.Load() != 2 {
		t.Fatalf("recovered control generated %d child calls, want initial + one control", childCalls.Load())
	}
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "control-crash", control); err != nil {
		t.Fatal(err)
	}
	if childCalls.Load() != 2 {
		t.Fatal("retrying the same recovered control instruction generated again")
	}
	accepted := 0
	for _, entry := range subagentTestEntries(t, f, child.SessionID) {
		if entry.Message != nil && entry.Message.ContextKind == "astral.subagent_instruction" && strings.Contains(string(encode(entry.Message)), control.Input) {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("control instruction appears %d times in child history, want 1", accepted)
	}
}

func TestSubagentNormalRequestAfterCompactionKeepsOneCurrentIdentity(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("A concise retained summary.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "identity-recovery", strings.Repeat("Context retained before compacting this task. ", 2500), false)
	before := subagentTestFinished(t, f, child.SessionID)
	if err := f.service.Compact(f.ctx, child.SessionID, "Keep the delegated task."); err != nil {
		t.Fatal(err)
	}
	identityEntries := func() []string {
		result := []string{}
		for _, entry := range subagentTestEntries(t, f, child.SessionID) {
			if entry.Message != nil && entry.Message.ContextKind == identityKind {
				result = append(result, entry.ID)
			}
		}
		return result
	}
	identitiesBefore := identityEntries()
	control := AgentControlInput{AgentID: child.SessionID, Input: "A normal request after the compaction checkpoint."}
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "after-compact", control); err != nil {
		t.Fatal(err)
	}
	subagentRecoveryReceipt(t, f, child.SessionID, "subagent_instruction_after-compact")
	after := subagentTestFinished(t, f, child.SessionID)
	if after.LastRun.ID == before.LastRun.ID {
		t.Fatal("fixture did not execute a new post-compaction Run")
	}
	if identitiesAfter := identityEntries(); !reflect.DeepEqual(identitiesAfter, identitiesBefore) {
		t.Fatalf("ordinary request appended identity again: before=%v after=%v", identitiesBefore, identitiesAfter)
	}
	request := subagentTestRequest(t, f, child.SessionID)
	if len(request.Messages) == 0 || request.Messages[0].ContextKind != "summary" {
		t.Fatal("fixture did not preserve compacted context into the new request")
	}
	current := 0
	for _, message := range request.Messages {
		if message.ContextKind != identityKind {
			continue
		}
		var metadata identityMetadata
		if json.Unmarshal(message.Metadata, &metadata) == nil && metadata.SessionID == child.SessionID {
			if message.Role != core.RoleUser || metadata.ParentID != parent.id || metadata.CompactionID == "" {
				t.Fatalf("wrong current identity after compaction: %+v %+v", message, metadata)
			}
			current++
		}
	}
	if current != 1 {
		t.Fatalf("new request contains %d current identities after compaction, want 1", current)
	}
}

type subagentRecoveryScanCounter struct {
	core.Store
	sessionID string
	mu        sync.Mutex
	queries   []core.Query
	returned  map[string]bool
}

func (s *subagentRecoveryScanCounter) Scan(ctx context.Context, rolloutID string, query core.Query) (core.Page, error) {
	page, err := s.Store.Scan(ctx, rolloutID, query)
	if query.SessionID == s.sessionID {
		s.mu.Lock()
		s.queries = append(s.queries, query)
		if page.Next != "" {
			s.returned[page.Next] = true
		}
		s.mu.Unlock()
	}
	return page, err
}

func TestSubagentDeliveryResumesFromPublicCursorWithoutRescanningHistory(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("A report counted once.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "cursor-recovery", "Create one report before the long passive history.", false)
	state := subagentTestFinished(t, f, child.SessionID)
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	f.idle(t, parent.id)
	subagentRecoveryStopWorker(f)
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	reportIDs := func() []string {
		result := []string{}
		for _, entry := range subagentTestEntries(t, f, parent.id) {
			if entry.Message != nil && entry.Message.ContextKind == subagentUpdateKind {
				result = append(result, entry.ID)
			}
		}
		return result
	}
	beforeReports := reportIDs()
	if len(beforeReports) == 0 {
		t.Fatal("fixture has no acknowledged report to protect against replay")
	}
	appendPassive := func(inputID string, count int) {
		t.Helper()
		contexts := make([]core.Message, 0, count)
		for i := 0; i < count; i++ {
			message := core.User(fmt.Sprintf("Ordinary passive record %s %d.", inputID, i))
			message.ContextKind = "test.passive_history"
			contexts = append(contexts, message)
		}
		receipt, err := r.core.Load().InjectContext(f.ctx, core.Input{ID: inputID, Contexts: contexts})
		if err != nil || receipt.State != core.InputApplied {
			t.Fatalf("append passive history: receipt=%+v error=%v", receipt, err)
		}
		f.idle(t, child.SessionID)
	}
	// Replace only the Host's Store access. The open Core runtime keeps its
	// real configuration Store, so the counter measures delivery scans alone.
	counter := &subagentRecoveryScanCounter{Store: f.coreStore, sessionID: child.SessionID, returned: map[string]bool{}}
	f.service.CoreStore = counter
	appendPassive("long-history", 3*core.DefaultPageSize+17)
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	var checkpoint subagentCheckpoint
	if err := f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint); err != nil {
		t.Fatal(err)
	}
	counter.mu.Lock()
	initialPages, genuineCursor := len(counter.queries), counter.returned[checkpoint.Cursor]
	counter.queries = nil
	counter.mu.Unlock()
	if initialPages < 4 || checkpoint.Cursor == "" || !genuineCursor {
		t.Fatalf("long scan did not retain a public next cursor: pages=%d cursor=%q returned=%v", initialPages, checkpoint.Cursor, genuineCursor)
	}
	savedCursor := checkpoint.Cursor
	appendPassive("short-tail", 3)
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	counter.mu.Lock()
	queries := append([]core.Query(nil), counter.queries...)
	counter.mu.Unlock()
	firstCursor := ""
	if len(queries) > 0 {
		firstCursor = queries[0].Cursor
	}
	if len(queries) < 1 || len(queries) > 2 || firstCursor != savedCursor {
		t.Fatalf("small append rescanned the complete history: pages=%d first_cursor=%q saved_cursor=%q", len(queries), firstCursor, savedCursor)
	}
	if afterReports := reportIDs(); !reflect.DeepEqual(afterReports, beforeReports) {
		t.Fatalf("cursor tail reprocessing duplicated reports: before=%v after=%v", beforeReports, afterReports)
	}
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	counter.mu.Lock()
	unchangedPages := len(counter.queries)
	counter.mu.Unlock()
	if unchangedPages != len(queries) {
		t.Fatal("unchanged child version caused another history scan")
	}
}

func TestSubagentSharedForkRecoveryRestoresSavedModelBeforeInitialRun(t *testing.T) {
	var childCalls atomic.Int32
	var actualModel atomic.Value
	actualModel.Store("")
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "You are a subagent of Session") {
			childCalls.Add(1)
			actualModel.Store(request["model"].(string))
		}
		return textReply("Recovered fork result.")
	})
	parent := subagentTestSeed(t, f)
	parentRequest := subagentTestRequest(t, f, parent.id)
	parentSelection := parent.core.Load().Configuration().Selection
	override := anotherModel(t, f)
	subagentRecoveryStopWorker(f)
	child, err := f.service.spawnSubagent(f.ctx, parent, core.Invocation{SessionID: parent.id, ExecutionID: "fork-before-configure-crash"}, SpawnAgentInput{Task: "Use the explicit model after fork recovery.", InheritContext: true, Model: override.ID})
	if err != nil {
		t.Fatal(err)
	}
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Started || settings.Configuration.Selection.ID != override.ID {
		t.Fatalf("fixture did not retain the unstarted explicit model: %+v", settings)
	}
	parentState := parent.core.Load().State()
	before, err := f.coreStore.Scan(f.ctx, parentState.RolloutID, core.Query{PathHead: settings.ForkEntryID, Limit: 1000, MaxBytes: 128 << 20})
	if err != nil || before.Next != "" {
		t.Fatalf("capture inherited history: %+v %v", before, err)
	}
	// Persist only Core's shared Fork. The Host has saved M2, but the fork
	// still inherits the parent's M1 because Configure has not happened yet.
	if _, err := f.coreStore.Fork(f.ctx, core.ForkRequest{SourceID: parent.id, SourceRolloutID: parentState.RolloutID, TargetID: child.SessionID, HeadID: settings.ForkEntryID, Mode: core.ForkShared}); err != nil {
		t.Fatal(err)
	}
	forked, err := f.coreStore.LoadState(f.ctx, child.SessionID)
	if err != nil || forked.RolloutID != parentState.RolloutID {
		t.Fatalf("fixture did not persist the shared fork: %+v %v", forked, err)
	}
	projection, err := core.ReadContext(f.ctx, f.coreStore, forked.RolloutID, forked.Branch.HeadID, 128<<20)
	if err != nil || projection.Selection.ID != parentSelection.ID || projection.Selection.ID == override.ID || childCalls.Load() != 0 {
		t.Fatalf("fixture crossed Configure before the simulated crash: selection=%+v calls=%d error=%v", projection.Selection, childCalls.Load(), err)
	}
	f.restart(t)
	subagentRecoveryReceipt(t, f, child.SessionID, "subagent_initial_"+child.SessionID)
	recovered := subagentTestFinished(t, f, child.SessionID)
	session, err := f.service.Core(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Configuration().Selection.ID != override.ID || actualModel.Load().(string) != override.ModelID || childCalls.Load() != 1 {
		t.Fatalf("recovery lost explicit child model: selection=%+v wire_model=%v calls=%d", session.Configuration().Selection, actualModel.Load(), childCalls.Load())
	}
	request := subagentTestRequest(t, f, child.SessionID)
	if request.SystemPrompt != parentRequest.SystemPrompt || !reflect.DeepEqual(request.Tools, parentRequest.Tools) {
		t.Fatal("fork recovery changed the saved parent System or tool prefix")
	}
	after, err := f.coreStore.Scan(f.ctx, recovered.RolloutID, core.Query{PathHead: recovered.Branch.HeadID, Limit: 1000, MaxBytes: 128 << 20})
	if err != nil || after.Next != "" {
		t.Fatalf("read recovered fork history: %+v %v", after, err)
	}
	byID := map[string]core.Entry{}
	for _, record := range after.Records {
		byID[record.Entry.ID] = record.Entry
	}
	for _, record := range before.Records {
		if retained, ok := byID[record.Entry.ID]; !ok || !reflect.DeepEqual(retained, record.Entry) {
			t.Fatalf("fork recovery rewrote or dropped inherited entry %s", record.Entry.ID)
		}
	}
}

package harness

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/wake"
)

// These integration tests exercise the Harness/Core handoff independently of
// the wake package's HTTP admission and generation-controlled batching worker.
func directWakeFixture(t *testing.T, generate func(context.Context, int, map[string]any) any) *fixture {
	t.Helper()
	f := newFixture(t, 0, generate)
	detachWakeWorker(t, f)
	return f
}

func detachWakeWorker(t *testing.T, f *fixture) {
	t.Helper()
	service := f.service.WakeEndpoints
	f.service.WakeEndpoints = nil
	if service != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := service.Close(ctx); err != nil {
				t.Error(err)
			}
		})
	}
}

func externalBatch(f *fixture, id string) wake.Batch {
	at := time.Date(2026, 11, 8, 3, 0, 0, 0, time.UTC)
	return wake.Batch{ID: id, Digest: digest(id), AgentID: f.agent.ID, SessionID: f.session.ID, Time: at, Events: []wake.BatchEvent{{EndpointID: "synthetic-game-endpoint", ComputerID: "synthetic-computer", ComputerName: "Synthetic game computer", EventID: id + "-event", Title: "Game events", Prompt: "Handle messages within the current task.", Text: "Player joined the synthetic game.", ReceivedAt: at}}}
}

func requireExternalHandled(t *testing.T, f *fixture, batch wake.Batch) {
	t.Helper()
	batch.Events = nil
	var result wake.DeliveryResult
	eventually(t, f.ctx, func() bool {
		var err error
		result, err = f.service.deliverWake(f.ctx, batch)
		if err != nil {
			t.Error(err)
			return true
		}
		return result.Handled
	})
	if !result.Accepted || !result.Handled {
		t.Fatalf("accepted batch never reached a model request: %+v", result)
	}
}

func TestExternalEventsWakeIdleRootOnceAndKeepSourceIdentity(t *testing.T) {
	f := directWakeFixture(t, func(context.Context, int, map[string]any) any { return textReply("Done") })
	batch := externalBatch(f, "synthetic-idle-events")
	result, err := f.service.deliverWake(f.ctx, batch)
	if err != nil || !result.Accepted {
		t.Fatalf("idle root rejected its batch: %+v %v", result, err)
	}
	f.idle(t, f.session.ID)
	requireExternalHandled(t, f, batch)
	if f.count() != 1 {
		t.Fatalf("one batch produced %d requests", f.count())
	}
	receipt, err := f.coreStore.LookupInput(f.ctx, f.session.ID, batch.ID)
	if err != nil || receipt.Kind != core.InputContext || len(receipt.Contexts) != 1 {
		t.Fatalf("event was not durable passive context: %+v %v", receipt, err)
	}
	message := receipt.Contexts[0]
	var metadata messages.Metadata
	if err := json.Unmarshal(message.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Source != "external" || metadata.EventID != batch.ID || message.ContextKind != "external.events" || !strings.Contains(message.Content[0].Text, messages.ExternalEventNotice) || !strings.Contains(message.Content[0].Text, `computer-name="Synthetic game computer"`) {
		t.Fatalf("external event lost its trusted source: %+v", message)
	}
	for range 3 {
		requireExternalHandled(t, f, batch)
	}
	if f.count() != 1 {
		t.Fatal("repeated delivery drove a second model request")
	}
	batch.Digest = digest("different content")
	if _, err := f.service.deliverWake(f.ctx, batch); err == nil {
		t.Fatal("batch identity accepted a different digest")
	}
}

func TestExternalBusyBatchJoinsNextNaturalRequestWithoutAnotherRun(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := directWakeFixture(t, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			select {
			case <-ctx.Done():
				return nil
			case <-release:
			}
			return toolReply("inspect-direct-delegates", "agent_control", map[string]any{})
		}
		return textReply("Done")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "synthetic-owner-task", Text: "Continue the synthetic task."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	runID := r.core.Load().State().LastRun.ID
	batch := externalBatch(f, "synthetic-busy-events")
	for range 3 {
		result, err := f.service.deliverWake(f.ctx, batch)
		if err != nil || !result.Accepted || result.Handled {
			t.Fatalf("busy event admission was not passive: %+v %v", result, err)
		}
	}
	if f.count() != 1 {
		t.Fatal("passive delivery launched an extra model request")
	}
	close(release)
	f.idle(t, f.session.ID)
	requireExternalHandled(t, f, batch)
	if f.count() != 2 || r.core.Load().State().LastRun.ID != runID {
		t.Fatalf("natural boundary opened a separate Run: requests=%d run=%+v", f.count(), r.core.Load().State().LastRun)
	}
	if _, err := f.coreStore.LookupInput(f.ctx, f.session.ID, batch.ID+":wakeup"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("already observed batch received another activation: %v", err)
	}
}

func TestExternalUnreadBatchGetsOneTailHandoffAfterBusyRunEnds(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := directWakeFixture(t, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			select {
			case <-ctx.Done():
				return nil
			case <-release:
			}
		}
		return textReply("Done")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "synthetic-ending-task", Text: "Finish the current synthetic task."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	batch := externalBatch(f, "synthetic-tail-events")
	for at := range 20 {
		event := batch.Events[0]
		event.EventID = domain.ID("synthetic-event")
		event.Text = strings.Repeat("Synthetic event ", at%3+1)
		if at == 0 {
			batch.Events[0] = event
		} else {
			batch.Events = append(batch.Events, event)
		}
	}
	if result, err := f.service.deliverWake(f.ctx, batch); err != nil || !result.Accepted {
		t.Fatalf("busy burst was not accepted: %+v %v", result, err)
	}
	close(release)
	f.idle(t, f.session.ID)
	if f.count() != 1 {
		t.Fatal("passive burst caused per-event requests before handoff")
	}
	requireExternalHandled(t, f, batch)
	f.idle(t, f.session.ID)
	if f.count() != 2 {
		t.Fatalf("20-event batch needed %d total model requests", f.count())
	}
	requireExternalHandled(t, f, batch)
	if f.count() != 2 {
		t.Fatal("tail handoff repeated")
	}
}

func TestExternalStopCoversOldBatchWhileNewEventsCanWake(t *testing.T) {
	entered := make(chan struct{})
	f := directWakeFixture(t, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			<-ctx.Done()
			return nil
		}
		return textReply("Done")
	})
	batch := externalBatch(f, "synthetic-stopped-events")
	if _, err := f.service.deliverWake(f.ctx, batch); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if err := f.service.Stop(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	requireExternalHandled(t, f, batch)
	if f.count() != 1 {
		t.Fatal("retry of a stopped batch resumed execution")
	}
	if _, err := f.service.deliverWake(f.ctx, externalBatch(f, "synthetic-new-events")); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if f.count() != 2 {
		t.Fatal("new external event could not activate after Stop")
	}
}

func TestExternalRecoveryUsesCoreReceiptWithoutReconstructingBody(t *testing.T) {
	f := directWakeFixture(t, func(context.Context, int, map[string]any) any { return textReply("Done") })
	batch := externalBatch(f, "synthetic-recovered-events")
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.core.Load().InjectContext(f.ctx, core.Input{ID: batch.ID, Digest: batch.Digest, Contexts: []core.Message{messages.ExternalEvents(batch, false)}}); err != nil {
		t.Fatal(err)
	}
	if f.count() != 0 {
		t.Fatal("passive context alone called the model")
	}
	f.restart(t)
	detachWakeWorker(t, f)
	batch.Events = nil
	requireExternalHandled(t, f, batch)
	f.idle(t, f.session.ID)
	if f.count() != 1 {
		t.Fatalf("recovered body was not handed off once: %d", f.count())
	}
	f.restart(t)
	detachWakeWorker(t, f)
	requireExternalHandled(t, f, batch)
	if f.count() != 1 {
		t.Fatal("handled batch woke again after restart")
	}
	missing := batch
	missing.ID, missing.Digest = "synthetic-missing-body", digest("missing body")
	if _, err := f.service.deliverWake(f.ctx, missing); err == nil {
		t.Fatal("missing Core receipt was reconstructed without an event body")
	}
}

// Stage the durable recovery facts independently: an old activation has been
// consumed, but its frozen request did not include this passive batch. Neither
// the consumed driver nor a compacted context is proof of model coverage.
func seedUnreadBatchAfterConsumedDriver(t *testing.T, f *fixture, batch wake.Batch) {
	t.Helper()
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := batch.ID + ":wakeup"
	r.mu.Lock()
	_, err = r.accept(f.ctx, core.Input{ID: id, Digest: digest(struct{ BatchID string }{batch.ID}), Message: messages.ExternalTrigger(id, batch.ID)}, core.InputSteer)
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if _, err := r.core.Load().InjectContext(f.ctx, core.Input{ID: batch.ID, Digest: batch.Digest, Contexts: []core.Message{messages.ExternalEvents(batch, false)}}); err != nil {
		t.Fatal(err)
	}
}

func TestExternalConsumedDriverCanRecoverOnceAfterServerRestart(t *testing.T) {
	f := directWakeFixture(t, func(context.Context, int, map[string]any) any { return textReply("Done") })
	batch := externalBatch(f, "synthetic-consumed-driver")
	seedUnreadBatchAfterConsumedDriver(t, f, batch)
	f.restart(t)
	detachWakeWorker(t, f)
	batch.Events = nil
	requireExternalHandled(t, f, batch)
	f.idle(t, f.session.ID)
	if f.count() != 2 {
		t.Fatalf("old consumed driver did not get exactly one recovery: %d", f.count())
	}
	requireExternalHandled(t, f, batch)
	if f.count() != 2 {
		t.Fatal("recovery activation repeated")
	}
}

func TestExternalConsumedDriverCanRecoverOnceAfterCompaction(t *testing.T) {
	f := directWakeFixture(t, func(context.Context, int, map[string]any) any { return textReply("Done") })
	batch := externalBatch(f, "synthetic-compacted-driver")
	seedUnreadBatchAfterConsumedDriver(t, f, batch)
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Keep a generated recent block so the older activation/event group can
	// actually be summarized under the production 20k-token retention policy.
	if _, err := r.core.Load().InjectContext(f.ctx, core.Input{ID: "synthetic-recent-context", Contexts: []core.Message{core.User(strings.Repeat("Synthetic retained context. ", 12000))}}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Compact(f.ctx, f.session.ID, "Retain the external events."); err != nil {
		t.Fatal(err)
	}
	batch.Events = nil
	requireExternalHandled(t, f, batch)
	f.idle(t, f.session.ID)
	if f.count() != 3 {
		t.Fatalf("compaction did not permit a single unread recovery: %d", f.count())
	}
	requireExternalHandled(t, f, batch)
	if f.count() != 3 {
		t.Fatal("compaction recovery activation repeated")
	}
}

func TestExternalPreparationFailureDoesNotRedrivePendingActivation(t *testing.T) {
	f := directWakeFixture(t, func(context.Context, int, map[string]any) any { return textReply("Done") })
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	batch := externalBatch(f, "synthetic-preparation-failure")
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.core.Load().InjectContext(f.ctx, core.Input{ID: batch.ID, Digest: batch.Digest, Contexts: []core.Message{messages.ExternalEvents(batch, false)}}); err != nil {
		t.Fatal(err)
	}
	id := batch.ID + ":wakeup"
	if _, err := r.core.Load().Steer(f.ctx, core.Input{ID: id, Digest: digest(struct{ BatchID string }{batch.ID}), Message: messages.ExternalTrigger(id, batch.ID)}); err != nil {
		t.Fatal(err)
	}
	profile.Overrides = map[string]json.RawMessage{"context_window": encode(8192), "max_output_tokens": encode(4096)}
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the public durable boundary: Run started and failed before
	// consuming its queued driver or freezing any generation request. This is
	// the same recovery-fixture technique used for interrupted subagent starts.
	state, err := f.coreStore.LoadState(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	runID := "synthetic-failed-preparation-run"
	entries := []core.Entry{
		{ID: "synthetic-preparation-started", Kind: core.EntryRun, RunID: runID, Run: &core.RunRecord{ID: runID, Status: "started"}},
		{ID: "synthetic-preparation-failed", Kind: core.EntryRun, RunID: runID, Run: &core.RunRecord{ID: runID, Status: "failed", Error: "synthetic request preparation failed before freeze"}},
	}
	for at := range entries {
		entries[at].SessionID, entries[at].Version, entries[at].Time = f.session.ID, state.Version+1, time.Now().UTC()
	}
	if _, err := f.coreStore.Commit(f.ctx, f.session.ID, core.Commit{ID: "synthetic-preparation-window", ExpectedVersion: state.Version, Entries: entries}); err != nil {
		t.Fatal(err)
	}
	f.service = New(f.store, f.coreStore, f.service.Models, f.hub)
	detachWakeWorker(t, f)
	r, err = f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	failed := r.core.Load().State()
	if f.count() != 0 {
		t.Fatal("synthetic pre-freeze failure reached the model provider")
	}
	batch.Events = nil
	for range 32 {
		result, err := f.service.deliverWake(f.ctx, batch)
		if err != nil || !result.Accepted || result.Handled {
			t.Fatalf("failed preparation was retried or counted as handled: %+v %v", result, err)
		}
	}
	settled := r.core.Load().State()
	if settled.Version != failed.Version || settled.LastRun.ID != failed.LastRun.ID {
		t.Fatalf("unchanged failure created more Core writes: before=%+v after=%+v", failed.LastRun, settled.LastRun)
	}
	profile.Overrides["context_window"] = encode(128000)
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Continue(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	requireExternalHandled(t, f, batch)
	if f.count() != 1 {
		t.Fatalf("explicit Continue did not consume the retained input once: %d", f.count())
	}
}

func TestExternalEventsRejectForeignAndInternalSubagentTargets(t *testing.T) {
	f := directWakeFixture(t, func(context.Context, int, map[string]any) any { return textReply("Done") })
	batch := externalBatch(f, "synthetic-scope-events")
	batch.AgentID = "synthetic-foreign-bot"
	if _, err := f.service.deliverWake(f.ctx, batch); err == nil {
		t.Fatal("foreign Bot batch reached this Session")
	}
	child := domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: f.agent.ID, SubagentParentID: f.session.ID, Title: "Synthetic child"}
	if err := f.store.Save(f.ctx, "sessions", &child.Meta, &child); err != nil {
		t.Fatal(err)
	}
	batch.AgentID, batch.SessionID = f.agent.ID, child.ID
	if _, err := f.service.deliverWake(f.ctx, batch); err == nil {
		t.Fatal("external batch bypassed direct-parent subagent control")
	}
	if f.count() != 0 {
		t.Fatal("scope rejection activated a model")
	}
}

func acceptedExternalFlow(t *testing.T, f *fixture) wake.Batch {
	t.Helper()
	var batch wake.Batch
	eventually(t, f.ctx, func() bool {
		rows, err := f.store.PendingMaintenance(f.ctx, "wake_buffer")
		if err != nil {
			t.Error(err)
			return true
		}
		for _, row := range rows {
			var flow struct {
				SessionID string      `json:"session_id"`
				Inflight  *wake.Batch `json:"inflight"`
			}
			if json.Unmarshal(row, &flow) != nil || flow.SessionID != f.session.ID || flow.Inflight == nil || len(flow.Inflight.Events) != 0 {
				continue
			}
			if _, err := f.coreStore.LookupInput(f.ctx, f.session.ID, flow.Inflight.ID); err == nil {
				batch = *flow.Inflight
				return true
			}
		}
		return false
	})
	if batch.ID == "" {
		t.Fatal("external flow never relinquished its accepted body to Core")
	}
	return batch
}

func realExternalEndpoint(t *testing.T, f *fixture) domain.WakeEndpoint {
	t.Helper()
	ctx := persistence.WithSessionScope(f.ctx, f.agent.ID, f.session.ID)
	ctx = wake.WithComputerID(ctx, f.daemons[0].Credentials.ComputerID)
	endpoint, err := f.service.WakeEndpoints.Create(ctx, f.agent.ID, wake.CreateInput{Title: "Synthetic game bridge", Prompt: "Handle game events within the current task.", Notes: "/synthetic/game-bridge.js"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.WakeEndpoints.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func TestExternalWorkerRestoresUnreadAcceptedBatchAfterNormalServerClose(t *testing.T) {
	entered := make(chan struct{})
	f := newFixture(t, 1, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			<-ctx.Done()
			return nil
		}
		return textReply("Done")
	})
	endpoint := realExternalEndpoint(t, f)
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "synthetic-before-restart", Text: "Complete the running task."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if _, err := f.service.WakeEndpoints.Accept(f.ctx, endpoint.ComputerID, endpoint.ID, wake.Event{EventID: "synthetic-restart-event", Text: "Player arrived while the model was busy."}); err != nil {
		t.Fatal(err)
	}
	batch := acceptedExternalFlow(t, f)
	f.restart(t)
	if err := f.service.WakeEndpoints.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		rows, err := f.store.PendingMaintenance(f.ctx, "wake_buffer")
		if err != nil {
			t.Error(err)
			return true
		}
		return len(rows) == 0
	})
	f.idle(t, f.session.ID)
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if handled, err := NewIMInspector(r.core.Load(), f.coreStore).handled(f.ctx, batch.ID, true); err != nil || !handled {
		t.Fatalf("Server cancellation lost the unread event: handled=%v err=%v", handled, err)
	}
}

func TestExternalWorkerStopInvalidatesOldGenerationBeforeFreshEvents(t *testing.T) {
	entered := make(chan struct{})
	f := newFixture(t, 1, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			<-ctx.Done()
			return nil
		}
		return textReply("Done")
	})
	endpoint := realExternalEndpoint(t, f)
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "synthetic-before-stop", Text: "Run until stopped."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	event := wake.Event{EventID: "synthetic-old-event", Text: "An event before Stop."}
	if _, err := f.service.WakeEndpoints.Accept(f.ctx, endpoint.ComputerID, endpoint.ID, event); err != nil {
		t.Fatal(err)
	}
	batch := acceptedExternalFlow(t, f)
	if err := f.service.Stop(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if result, err := f.service.deliverWake(f.ctx, batch); err != nil || !result.Handled || result.Accepted {
		t.Fatalf("old worker generation survived Stop: %+v %v", result, err)
	}
	if _, err := f.service.WakeEndpoints.Accept(f.ctx, endpoint.ComputerID, endpoint.ID, event); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatal("duplicate pre-Stop event woke the stopped Session")
	}
	if _, err := f.service.WakeEndpoints.Accept(f.ctx, endpoint.ComputerID, endpoint.ID, wake.Event{EventID: "synthetic-fresh-event", Text: "A genuinely new event after Stop."}); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool { return f.count() >= 2 })
	f.idle(t, f.session.ID)
}

func TestPermanentDeletionForgetsExternalBuffersAndInflightWorkers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tree     bool
		agent    bool
		inflight bool
	}{{name: "root-inflight-buffer", inflight: true}, {name: "root-tree-inflight-buffer", tree: true, inflight: true}, {name: "bot-inflight-buffer", agent: true, inflight: true}} {
		t.Run(tc.name, func(t *testing.T) {
			entered := make(chan struct{})
			f := newFixture(t, 1, func(ctx context.Context, n int, _ map[string]any) any {
				if n == 1 {
					close(entered)
					<-ctx.Done()
					return nil
				}
				return textReply("Done")
			})
			ctx := persistence.WithSessionScope(f.ctx, f.agent.ID, f.session.ID)
			ctx = wake.WithComputerID(ctx, f.daemons[0].Credentials.ComputerID)
			endpoint, err := f.service.WakeEndpoints.Create(ctx, f.agent.ID, wake.CreateInput{Title: "Synthetic deletion bridge"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.inflight {
				if err := f.service.WakeEndpoints.Start(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "synthetic-delete-active", Text: "Run until deletion."}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			if tc.tree {
				parent, err := f.service.open(f.ctx, f.session.ID)
				if err != nil {
					t.Fatal(err)
				}
				child := subagentTestSpawn(t, f, parent, "synthetic-delete-tree", "Synthetic independently completed work.", false)
				f.idle(t, child.SessionID)
			}
			events := []wake.Event{{EventID: "synthetic-delete-first", Text: "An event before deletion."}}
			if _, err := f.service.WakeEndpoints.Accept(f.ctx, endpoint.ComputerID, endpoint.ID, events[0]); err != nil {
				t.Fatal(err)
			}
			if tc.inflight {
				acceptedExternalFlow(t, f)
				events = append(events, wake.Event{EventID: "synthetic-delete-buffered", Text: "Buffered behind the accepted batch."})
				if _, err := f.service.WakeEndpoints.Accept(f.ctx, endpoint.ComputerID, endpoint.ID, events[1]); err != nil {
					t.Fatal(err)
				}
			}
			before := f.count()
			if tc.agent {
				if err := f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err != nil {
					t.Fatal(err)
				}
			} else {
				record := archiveForDeletion(t, f, f.session.ID)
				if err := f.service.DeleteSession(f.ctx, record.ID, record.Revision); err != nil {
					t.Fatal(err)
				}
			}
			// Join any callback already outside the worker lock, then inspect
			// all durable kinds, including complete receipts and status rows.
			f.service.WakeEndpoints.WakeSession(f.session.ID)
			if err := f.service.WakeEndpoints.Close(f.ctx); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"wake_buffer", "wake_status"} {
				id := f.session.ID
				if kind == "wake_status" {
					id = endpoint.ID
				}
				var row any
				if err := f.store.Maintenance(f.ctx, kind, id, &row); !domain.IsMissing(err) {
					t.Fatalf("permanent deletion recreated %s: %v %+v", kind, err, row)
				}
			}
			for _, event := range events {
				key := sha256.Sum256([]byte(endpoint.ID + "\x00" + event.EventID))
				var row any
				if err := f.store.Maintenance(f.ctx, "wake_receipt", fmt.Sprintf("%x", key), &row); !domain.IsMissing(err) {
					t.Fatalf("permanent deletion retained event receipt: %v %+v", err, row)
				}
			}
			var endpointRecord domain.WakeEndpoint
			if err := f.store.Get(f.ctx, wake.Kind, endpoint.ID, &endpointRecord); !domain.IsMissing(err) {
				t.Fatalf("permanent deletion retained event endpoint: %+v %v", endpointRecord, err)
			}
			if f.count() != before {
				t.Fatalf("deleted external flow launched another model request: %d -> %d", before, f.count())
			}
		})
	}
}

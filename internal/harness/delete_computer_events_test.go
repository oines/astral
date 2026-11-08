package harness

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/wake"
)

// Public Host persistence seeds an accepted-but-not-acknowledged delivery
// boundary. No large replay payload or private wake service state is required.
type computerEventFlowFixture struct {
	AgentID    string            `json:"agent_id"`
	SessionID  string            `json:"session_id"`
	Generation uint64            `json:"generation"`
	Buffer     []wake.BatchEvent `json:"buffer,omitempty"`
	Bytes      int               `json:"bytes"`
	Due        time.Time         `json:"due"`
	Flood      bool              `json:"flood,omitempty"`
	Inflight   *wake.Batch       `json:"inflight,omitempty"`
}

func seedComputerEvents(t *testing.T, f *fixture) (computerEventFlowFixture, []domain.WakeEndpoint) {
	t.Helper()
	ids := []string{f.daemons[0].Credentials.ComputerID, f.daemons[1].Credentials.ComputerID}
	if err := f.service.SelectComputer(f.ctx, f.session.ID, ids[1], ""); err != nil {
		t.Fatal(err)
	}
	endpoints := []domain.WakeEndpoint{}
	for i, id := range ids {
		endpoint := domain.WakeEndpoint{Meta: domain.Meta{ID: []string{"deleted-computer-hook", "surviving-computer-hook"}[i]}, AgentID: f.agent.ID, ComputerID: id, SessionID: f.session.ID, SourceSessionID: f.session.ID, Title: []string{"Deleted source", "Surviving source"}[i], Enabled: true}
		if err := f.store.Save(f.ctx, wake.Kind, &endpoint.Meta, &endpoint); err != nil {
			t.Fatal(err)
		}
		endpoints = append(endpoints, endpoint)
	}
	events := func(prefix string) []wake.BatchEvent {
		return []wake.BatchEvent{
			{EndpointID: endpoints[0].ID, ComputerID: ids[0], ComputerName: "Deleted synthetic source", EventID: prefix + "-deleted", Title: endpoints[0].Title, Text: prefix + "_DELETED_SOURCE", ReceivedAt: time.Now().UTC()},
			{EndpointID: endpoints[1].ID, ComputerID: ids[1], ComputerName: "Surviving synthetic source", EventID: prefix + "-surviving", Title: endpoints[1].Title, Text: prefix + "_SURVIVING_SOURCE", ReceivedAt: time.Now().UTC()},
		}
	}
	batch := wake.Batch{ID: "computer-delete-mixed-batch", Digest: digest("immutable mixed computer batch"), AgentID: f.agent.ID, SessionID: f.session.ID, Generation: 7, Time: time.Now().UTC(), Events: events("INFLIGHT")}
	flow := computerEventFlowFixture{AgentID: f.agent.ID, SessionID: f.session.ID, Generation: 7, Inflight: &batch, Buffer: events("BUFFER"), Bytes: 512, Due: time.Now().Add(time.Hour)}
	if err := f.store.SaveMaintenance(f.ctx, "wake_buffer", f.session.ID, "pending", flow); err != nil {
		t.Fatal(err)
	}
	return flow, endpoints
}

func revokeComputerForEvents(t *testing.T, f *fixture) domain.Computer {
	t.Helper()
	id := f.daemons[0].Credentials.ComputerID
	if err := f.hub.Revoke(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	var computer domain.Computer
	if err := f.store.Get(f.ctx, "computers", id, &computer); err != nil {
		t.Fatal(err)
	}
	return computer
}

func assertComputerEventCleanup(t *testing.T, f *fixture, original computerEventFlowFixture, endpoints []domain.WakeEndpoint, accepted bool) computerEventFlowFixture {
	t.Helper()
	var endpoint domain.WakeEndpoint
	if err := f.store.Get(f.ctx, wake.Kind, endpoints[0].ID, &endpoint); !domain.IsMissing(err) {
		t.Fatalf("deleted Computer retained its endpoint: %v", err)
	}
	if err := f.store.Version(f.ctx, wake.Kind, endpoints[0].ID, 1, &endpoint); !domain.IsMissing(err) {
		t.Fatalf("deleted Computer retained endpoint history: %v", err)
	}
	if err := f.store.Get(f.ctx, wake.Kind, endpoints[1].ID, &endpoint); err != nil || !endpoint.Enabled {
		t.Fatalf("other Computer endpoint changed: %+v %v", endpoint, err)
	}
	var flow computerEventFlowFixture
	if err := f.store.Maintenance(f.ctx, "wake_buffer", f.session.ID, &flow); err != nil {
		t.Fatal(err)
	}
	if flow.Generation == original.Generation || flow.Inflight == nil || len(flow.Buffer) != 1 || flow.Buffer[0].ComputerID != endpoints[1].ComputerID {
		t.Fatalf("deletion damaged the shared flow: %+v", flow)
	}
	if accepted {
		if flow.Inflight.ID != original.Inflight.ID || flow.Inflight.Digest != original.Inflight.Digest || len(flow.Inflight.Events) != 0 {
			t.Fatalf("already accepted input was rebuilt: %+v", flow.Inflight)
		}
	} else if flow.Inflight.ID == original.Inflight.ID || flow.Inflight.Digest == original.Inflight.Digest || len(flow.Inflight.Events) != 1 || flow.Inflight.Events[0].ComputerID != endpoints[1].ComputerID {
		t.Fatalf("unaccepted mixed batch was not rebuilt for the surviving source: %+v", flow.Inflight)
	}
	return flow
}

func TestDeleteComputerEventsPreserveSharedFlowAcrossRestart(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-yet-in-core", true: "core-committed-host-ack-missing"}[accepted], func(t *testing.T) {
			f := newFixture(t, 2, func(context.Context, int, map[string]any) any { return textReply("Handled surviving events.") })
			original, endpoints := seedComputerEvents(t, f)
			if accepted {
				r, err := f.service.open(f.ctx, f.session.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := r.core.Load().InjectContext(f.ctx, core.Input{ID: original.Inflight.ID, Digest: original.Inflight.Digest, Contexts: []core.Message{messages.ExternalEvents(*original.Inflight, false)}}); err != nil {
					t.Fatal(err)
				}
			}
			// Receipt inspection and Host cleanup must work from cold storage;
			// deleting a source is not a reason to restore a model/history runtime.
			f.restart(t)
			computer := revokeComputerForEvents(t, f)
			if err := f.service.DeleteComputer(f.ctx, computer.ID, computer.Revision); err != nil {
				t.Fatal(err)
			}
			f.service.mu.Lock()
			opened := len(f.service.runtimes)
			f.service.mu.Unlock()
			if opened != 0 || f.count() != 0 {
				t.Fatalf("Computer deletion opened cold execution: runtimes=%d calls=%d", opened, f.count())
			}
			next := assertComputerEventCleanup(t, f, original, endpoints, accepted)
			if !accepted {
				if _, err := f.coreStore.LookupInput(f.ctx, f.session.ID, original.Inflight.ID); !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("deleted unaccepted batch entered Core: %v", err)
				}
			}
			f.restart(t)
			if err := f.service.WakeEndpoints.Start(f.ctx); err != nil {
				t.Fatal(err)
			}
			eventually(t, f.ctx, func() bool { return f.count() == 1 })
			f.idle(t, f.session.ID)
			if _, err := f.coreStore.LookupInput(f.ctx, f.session.ID, next.Inflight.ID); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			requests := string(encode(f.requests))
			f.mu.Unlock()
			if !strings.Contains(requests, "INFLIGHT_SURVIVING_SOURCE") || strings.Contains(requests, "BUFFER_DELETED_SOURCE") || !accepted && strings.Contains(requests, "INFLIGHT_DELETED_SOURCE") {
				t.Fatal("restart delivered a removed source event or omitted the surviving source")
			}
			if accepted && !strings.Contains(requests, "INFLIGHT_DELETED_SOURCE") {
				t.Fatal("Computer deletion rewrote context already owned by Core")
			}
			if err := f.service.DeleteComputer(f.ctx, computer.ID, computer.Revision); err != nil {
				t.Fatal("idempotent retry:", err)
			}
		})
	}
}

type computerEventLookupStore struct {
	core.Store
	lookup func(context.Context, string, string) (core.InputReceipt, error)
}

func (s computerEventLookupStore) LookupInput(ctx context.Context, sessionID, id string) (core.InputReceipt, error) {
	return s.lookup(ctx, sessionID, id)
}

func TestDeleteComputerEventsFailClosedWhenCoreReceiptCannotBeRead(t *testing.T) {
	f := newFixture(t, 2, func(context.Context, int, map[string]any) any { return textReply("unused") })
	original, endpoints := seedComputerEvents(t, f)
	computer := revokeComputerForEvents(t, f)
	problem := errors.New("synthetic temporary Core receipt read failure")
	f.service.CoreStore = computerEventLookupStore{Store: f.coreStore, lookup: func(context.Context, string, string) (core.InputReceipt, error) { return core.InputReceipt{}, problem }}
	if err := f.service.DeleteComputer(f.ctx, computer.ID, computer.Revision); !errors.Is(err, problem) {
		t.Fatalf("receipt uncertainty was guessed away: %v", err)
	}
	var retained domain.Computer
	if err := f.store.Get(f.ctx, "computers", computer.ID, &retained); err != nil {
		t.Fatal("failed deletion removed Computer:", err)
	}
	var endpoint domain.WakeEndpoint
	if err := f.store.Get(f.ctx, wake.Kind, endpoints[0].ID, &endpoint); err != nil {
		t.Fatal("failed deletion removed endpoint:", err)
	}
	var flow computerEventFlowFixture
	if err := f.store.Maintenance(f.ctx, "wake_buffer", f.session.ID, &flow); err != nil || flow.Generation != original.Generation || len(flow.Inflight.Events) != 2 {
		t.Fatalf("failed deletion changed its immutable batch: %+v %v", flow, err)
	}
	f.service.CoreStore = f.coreStore
	if err := f.service.DeleteComputer(f.ctx, computer.ID, computer.Revision); err != nil {
		t.Fatal(err)
	}
	assertComputerEventCleanup(t, f, original, endpoints, false)
}

func TestDeleteComputerEventsSerializeAdmissionWithoutCancellingOrdinaryRun(t *testing.T) {
	entered, release, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	f := newFixture(t, 2, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				close(cancelled)
				return nil
			}
		}
		return textReply("Ordinary task continues.")
	})
	original, endpoints := seedComputerEvents(t, f)
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "ordinary-run", Text: "Keep the current model request running."}); err != nil {
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
	computer := revokeComputerForEvents(t, f)
	lookupEntered, lookupRelease := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.service.CoreStore = computerEventLookupStore{Store: f.coreStore, lookup: func(ctx context.Context, sessionID, id string) (core.InputReceipt, error) {
		if id == original.Inflight.ID {
			once.Do(func() { close(lookupEntered) })
			select {
			case <-lookupRelease:
			case <-ctx.Done():
				return core.InputReceipt{}, ctx.Err()
			}
		}
		return f.coreStore.LookupInput(ctx, sessionID, id)
	}}
	deleted := make(chan error, 1)
	go func() { deleted <- f.service.DeleteComputer(f.ctx, computer.ID, computer.Revision) }()
	select {
	case <-lookupEntered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	stale := make(chan error, 1)
	go func() {
		result, err := f.service.deliverWake(f.ctx, *original.Inflight)
		if err == nil && !result.Handled {
			err = errors.New("stale worker retained delivery ownership")
		}
		stale <- err
	}()
	close(lookupRelease)
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	select {
	case err := <-stale:
		if err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	select {
	case <-cancelled:
		t.Fatal("Computer event cleanup cancelled an ordinary model request")
	default:
	}
	if snapshot := r.core.Load().Snapshot(); !snapshot.Running || snapshot.Stopping {
		t.Fatalf("ordinary Run was stopped: %+v", snapshot)
	}
	assertComputerEventCleanup(t, f, original, endpoints, false)
	if _, err := f.coreStore.LookupInput(f.ctx, f.session.ID, original.Inflight.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("stale callback admitted removed source: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	f.idle(t, f.session.ID)
}

func TestDeleteMissingComputerStillCleansResidualEvents(t *testing.T) {
	f := newFixture(t, 2, func(context.Context, int, map[string]any) any { return textReply("unused") })
	original, endpoints := seedComputerEvents(t, f)
	computer := revokeComputerForEvents(t, f)
	if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error { return tx.Purge(f.ctx, "computers", computer.ID, computer.Revision) }); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteComputer(f.ctx, computer.ID, computer.Revision); err != nil {
		t.Fatal(err)
	}
	assertComputerEventCleanup(t, f, original, endpoints, false)
}

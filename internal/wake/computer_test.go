package wake

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func computerEndpoints(t *testing.T, f *fixture) (domain.WakeEndpoint, domain.WakeEndpoint) {
	t.Helper()
	a := f.create(t)
	b := domain.Computer{Meta: domain.Meta{ID: "computer_b"}, Name: "Other Computer"}
	if err := f.store.Save(f.ctx, "computers", &b.Meta, &b); err != nil {
		t.Fatal(err)
	}
	ctx := WithComputerID(persistence.WithSessionScope(context.Background(), f.agent.ID, f.session.ID), b.ID)
	other, err := f.s.Create(ctx, f.agent.ID, CreateInput{Title: "Independent listener"})
	if err != nil {
		t.Fatal(err)
	}
	return a, other
}

func revokeSource(t *testing.T, f *fixture, id string) {
	t.Helper()
	var c domain.Computer
	if err := f.store.Get(f.ctx, "computers", id, &c); err != nil {
		t.Fatal(err)
	}
	c.Revoked = true
	if err := f.store.Save(f.ctx, "computers", &c.Meta, &c); err != nil {
		t.Fatal(err)
	}
}

func purgeComputer(f *fixture, id string) func(*persistence.Tx) error {
	return func(tx *persistence.Tx) error {
		var c domain.Computer
		if err := tx.Get(f.ctx, "computers", id, &c); domain.IsMissing(err) {
			return nil
		} else if err != nil {
			return err
		}
		return tx.Purge(f.ctx, "computers", id, c.Revision)
	}
}

func acceptComputerEvent(t *testing.T, f *fixture, e domain.WakeEndpoint, id string) {
	t.Helper()
	if _, err := f.s.Accept(f.ctx, e.ComputerID, e.ID, Event{EventID: id, Text: id}); err != nil {
		t.Fatal(err)
	}
}

func TestComputerDeletionFiltersSharedBufferAndPermanentlyRemovesEndpointConfiguration(t *testing.T) {
	f := manualWorkerFixture(t, nil)
	a, b := computerEndpoints(t, f)
	originalRevision := a.Revision
	notes := "Remove current and historical configuration"
	var err error
	a, err = f.s.Update(f.ctx, f.agent.ID, a.ID, Patch{Revision: a.Revision, Notes: &notes})
	if err != nil {
		t.Fatal(err)
	}
	acceptComputerEvent(t, f, a, "removed")
	acceptComputerEvent(t, f, b, "retained")
	if _, err := f.s.SnapshotComputerTargets(f.ctx, a.ComputerID); !archiveConflict(err) {
		t.Fatalf("live source did not require revocation: %v", err)
	}
	if err := f.s.WithComputerDeletion(f.ctx, a.ComputerID, nil, func(*persistence.Tx) error {
		t.Fatal("live source reached the permanent deletion transaction")
		return nil
	}); !archiveConflict(err) {
		t.Fatalf("live source endpoint deletion did not require revocation: %v", err)
	}
	revokeSource(t, f, a.ComputerID)
	ids, err := f.s.SnapshotComputerTargets(f.ctx, a.ComputerID)
	if err != nil || !reflect.DeepEqual(ids, []string{f.session.ID}) {
		t.Fatalf("shared target snapshot: %v %v", ids, err)
	}
	if err := f.s.WithComputerDeletion(f.ctx, a.ComputerID, nil, purgeComputer(f, a.ComputerID)); err != nil {
		t.Fatal(err)
	}
	var endpoint domain.WakeEndpoint
	if err := f.store.Get(f.ctx, Kind, a.ID, &endpoint); !domain.IsMissing(err) {
		t.Fatalf("deleted source endpoint remains: %v", err)
	}
	for _, revision := range []int64{originalRevision, a.Revision} {
		if err := f.store.Version(f.ctx, Kind, a.ID, revision, &endpoint); !domain.IsMissing(err) {
			t.Fatalf("deleted source endpoint revision remains: %d %v", revision, err)
		}
	}
	var state endpointState
	if err := f.store.Maintenance(f.ctx, stateKind, a.ID, &state); !domain.IsMissing(err) {
		t.Fatalf("deleted endpoint receiving status remains: %v", err)
	}
	for _, e := range []domain.WakeEndpoint{a, b} {
		var receipt eventReceipt
		id := "removed"
		if e.ID == b.ID {
			id = "retained"
		}
		if err := f.store.Maintenance(f.ctx, receiptKind, eventKey(e.ID, id), &receipt); err != nil {
			t.Fatalf("computer deletion erased an acceptance receipt: %v", err)
		}
	}
	if _, err := f.s.Get(f.ctx, f.agent.ID, b.ID); err != nil {
		t.Fatalf("other source endpoint was removed: %v", err)
	}
	workerClock(f, MergeWindow)
	f.s.cycle(f.ctx)
	batch := receive(t, f.delivered)
	if len(batch.Events) != 1 || batch.Events[0].ComputerID != b.ComputerID || batch.Events[0].EventID != "retained" {
		t.Fatalf("wrong mixed-buffer cleanup: %+v", batch)
	}
	if err := f.s.WithComputerDeletion(f.ctx, a.ComputerID, nil, purgeComputer(f, a.ComputerID)); err != nil {
		t.Fatalf("idempotent retry failed for a missing Computer: %v", err)
	}
}

func TestComputerDeletionRebuildsUnacceptedMixedBatchAndInvalidatesUnprocessedCallback(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	var delivered Batch
	f := manualWorkerFixture(t, func(_ context.Context, b Batch) (DeliveryResult, error) {
		calls++
		if calls == 1 {
			close(entered)
			<-release
			return DeliveryResult{}, nil
		}
		delivered = b
		return DeliveryResult{Accepted: true, Handled: true}, nil
	})
	a, b := computerEndpoints(t, f)
	acceptComputerEvent(t, f, a, "removed")
	acceptComputerEvent(t, f, b, "retained")
	workerClock(f, MergeWindow)
	go func() { f.s.cycle(f.ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("delivery callback did not start")
	}
	var before flow
	if err := f.store.Maintenance(f.ctx, flowKind, f.session.ID, &before); err != nil {
		t.Fatal(err)
	}
	revokeSource(t, f, a.ComputerID)
	if err := f.s.WithComputerDeletion(f.ctx, a.ComputerID, func(_ context.Context, sessionID, batchID, digest string) (bool, error) {
		if sessionID != f.session.ID || batchID != before.Inflight.ID || digest != before.Inflight.Digest {
			t.Fatal("receipt lookup was not bound to the original batch")
		}
		return false, nil
	}, purgeComputer(f, a.ComputerID)); err != nil {
		t.Fatal(err)
	}
	var after flow
	if err := f.store.Maintenance(f.ctx, flowKind, f.session.ID, &after); err != nil {
		t.Fatal(err)
	}
	if after.Generation == before.Generation || after.Inflight.ID == before.Inflight.ID || after.Inflight.Digest == before.Inflight.Digest || len(after.Inflight.Events) != 1 || after.Inflight.Events[0].ComputerID != b.ComputerID {
		t.Fatalf("unaccepted mixed batch was reused or lost: before=%+v after=%+v", before, after)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("old callback did not return")
	}
	f.s.mu.Lock()
	current := f.s.flows[f.session.ID]
	_, retry := f.s.retry[f.session.ID]
	valid := current != nil && current.Generation == after.Generation && current.Inflight.ID == after.Inflight.ID
	f.s.mu.Unlock()
	if !valid || retry {
		t.Fatalf("unprocessed callback restored old state: valid=%v retry=%v", valid, retry)
	}
	f.s.cycle(f.ctx)
	if calls != 2 || delivered.ID != after.Inflight.ID || len(delivered.Events) != 1 || delivered.Events[0].EventID != "retained" {
		t.Fatalf("retained source did not continue once: calls=%d batch=%+v", calls, delivered)
	}
}

func TestComputerDeletionUsesCoreReceiptWhenHostAcceptanceAcknowledgementFailed(t *testing.T) {
	accepted := map[string]string{}
	var batches []Batch
	var f *fixture
	f = manualWorkerFixture(t, func(_ context.Context, b Batch) (DeliveryResult, error) {
		batches = append(batches, b)
		if len(batches) == 1 {
			accepted[b.ID] = b.Digest // Simulate a committed Core InjectContext.
			db, err := sql.Open("sqlite", filepath.Join(f.store.Directory, "astral.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TRIGGER reject_computer_delete_ack BEFORE UPDATE ON operations WHEN NEW.kind='wake_buffer' BEGIN SELECT RAISE(ABORT, 'synthetic failed Host acknowledgement'); END`); err != nil {
				t.Fatal(err)
			}
			return DeliveryResult{Accepted: true}, nil
		}
		return DeliveryResult{Accepted: true, Handled: true}, nil
	})
	a, b := computerEndpoints(t, f)
	acceptComputerEvent(t, f, a, "already-in-context-a")
	acceptComputerEvent(t, f, b, "already-in-context-b")
	workerClock(f, MergeWindow)
	f.s.cycle(f.ctx)
	var persisted flow
	if err := f.store.Maintenance(f.ctx, flowKind, f.session.ID, &persisted); err != nil || persisted.Inflight == nil || len(persisted.Inflight.Events) != 2 {
		t.Fatalf("fixture did not retain the unacknowledged payload: %+v %v", persisted, err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.store.Directory, "astral.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TRIGGER reject_computer_delete_ack`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	acceptComputerEvent(t, f, a, "removed-future-a")
	acceptComputerEvent(t, f, b, "retained-future-b")
	revokeSource(t, f, a.ComputerID)
	if err = f.s.WithComputerDeletion(f.ctx, a.ComputerID, func(_ context.Context, _, batchID, digest string) (bool, error) {
		return accepted[batchID] == digest, nil
	}, purgeComputer(f, a.ComputerID)); err != nil {
		t.Fatal(err)
	}
	persisted = flow{}
	if err = f.store.Maintenance(f.ctx, flowKind, f.session.ID, &persisted); err != nil || persisted.Inflight.ID != batches[0].ID || persisted.Inflight.Digest != batches[0].Digest || len(persisted.Inflight.Events) != 0 || len(persisted.Buffer) != 1 || persisted.Buffer[0].EventID != "retained-future-b" {
		t.Fatalf("Core-accepted batch was rewritten or pending sources were lost: %+v %v", persisted, err)
	}
	f.s.cycle(f.ctx) // Finish the existing accepted batch without replaying it.
	workerClock(f, MergeWindow)
	f.s.cycle(f.ctx)
	if len(batches) != 3 || batches[1].ID != batches[0].ID || len(batches[1].Events) != 0 || batches[2].ID == batches[0].ID || len(batches[2].Events) != 1 || batches[2].Events[0].EventID != "retained-future-b" {
		t.Fatalf("another source from the accepted batch was replayed: %+v", batches)
	}
}

func TestComputerDeletionFailureLeavesComputerEndpointsAndFlowUnchanged(t *testing.T) {
	f := manualWorkerFixture(t, func(context.Context, Batch) (DeliveryResult, error) { return DeliveryResult{}, nil })
	a, b := computerEndpoints(t, f)
	acceptComputerEvent(t, f, a, "removed")
	acceptComputerEvent(t, f, b, "retained")
	workerClock(f, MergeWindow)
	f.s.cycle(f.ctx)
	revokeSource(t, f, a.ComputerID)
	var before flow
	if err := f.store.Maintenance(f.ctx, flowKind, f.session.ID, &before); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("synthetic receipt lookup failure")
	for _, failLookup := range []bool{true, false} {
		lookup := func(context.Context, string, string, string) (bool, error) { return false, nil }
		remove := func(tx *persistence.Tx) error {
			if err := purgeComputer(f, a.ComputerID)(tx); err != nil {
				return err
			}
			return failure
		}
		if failLookup {
			lookup = func(context.Context, string, string, string) (bool, error) { return false, failure }
		}
		if err := f.s.WithComputerDeletion(f.ctx, a.ComputerID, lookup, remove); !errors.Is(err, failure) {
			t.Fatalf("deletion did not propagate the pre-commit failure: %v", err)
		}
		var after flow
		if err := f.store.Maintenance(f.ctx, flowKind, f.session.ID, &after); err != nil || !reflect.DeepEqual(after, before) {
			t.Fatalf("failed deletion rewrote the durable flow: %+v %v", after, err)
		}
		if _, err := f.s.Get(f.ctx, f.agent.ID, a.ID); err != nil {
			t.Fatalf("failed deletion removed endpoint configuration: %v", err)
		}
		var c domain.Computer
		if err := f.store.Get(f.ctx, "computers", a.ComputerID, &c); err != nil {
			t.Fatalf("failed deletion removed the Computer: %v", err)
		}
	}
}

func TestComputerDeletionFindsResidualFlowWithoutEndpointAndSurvivesRestart(t *testing.T) {
	f := manualWorkerFixture(t, nil)
	a, b := computerEndpoints(t, f)
	acceptComputerEvent(t, f, a, "removed")
	acceptComputerEvent(t, f, b, "retained")
	if err := f.s.Delete(f.ctx, f.agent.ID, a.ID, a.Revision); err != nil {
		t.Fatal(err)
	}
	revokeSource(t, f, a.ComputerID)
	if err := f.store.Update(f.ctx, purgeComputer(f, a.ComputerID)); err != nil {
		t.Fatal(err)
	}
	ids, err := f.s.SnapshotComputerTargets(f.ctx, a.ComputerID)
	if err != nil || !reflect.DeepEqual(ids, []string{f.session.ID}) {
		t.Fatalf("orphaned flow provenance was not found: %v %v", ids, err)
	}
	if err := f.s.WithComputerDeletion(f.ctx, a.ComputerID, nil, purgeComputer(f, a.ComputerID)); err != nil {
		t.Fatal(err)
	}
	// Reconstruct only from committed Host state, without publishing the old
	// service's memory. The existing worker must continue the remaining source.
	f.s = New(f.store, func(_ context.Context, batch Batch) (DeliveryResult, error) {
		f.delivered <- batch
		return DeliveryResult{Accepted: true, Handled: true}, nil
	}, nil)
	if err := f.s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	batch := receive(t, f.delivered)
	if len(batch.Events) != 1 || batch.Events[0].EventID != "retained" || batch.Events[0].ComputerID != b.ComputerID {
		t.Fatalf("restart replayed the deleted source or lost another source: %+v", batch)
	}
}

func TestComputerDeletionDropsAnEntireUnacceptedSourceAndItsRetry(t *testing.T) {
	f := manualWorkerFixture(t, func(context.Context, Batch) (DeliveryResult, error) { return DeliveryResult{}, nil })
	e := f.create(t)
	acceptComputerEvent(t, f, e, "only-source")
	workerClock(f, MergeWindow)
	f.s.cycle(f.ctx)
	revokeSource(t, f, e.ComputerID)
	lookups := 0
	if err := f.s.WithComputerDeletion(f.ctx, e.ComputerID, func(context.Context, string, string, string) (bool, error) {
		lookups++
		return false, nil
	}, purgeComputer(f, e.ComputerID)); err != nil {
		t.Fatal(err)
	}
	var persisted flow
	if err := f.store.Maintenance(f.ctx, flowKind, f.session.ID, &persisted); !domain.IsMissing(err) {
		t.Fatalf("empty deleted-source flow remains durable: %+v %v", persisted, err)
	}
	f.s.mu.Lock()
	_, pending := f.s.flows[f.session.ID]
	_, retry := f.s.retry[f.session.ID]
	_, dirty := f.s.dirty[f.session.ID]
	f.s.mu.Unlock()
	if lookups != 1 || pending || retry || dirty {
		t.Fatalf("source cleanup left automatic work: lookups=%d pending=%v retry=%v dirty=%v", lookups, pending, retry, dirty)
	}
	if delay := f.s.cycle(f.ctx); delay != time.Hour {
		t.Fatalf("deleted source retained a hot retry timer: %s", delay)
	}
}

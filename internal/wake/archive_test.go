package wake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func archiveTarget(t *testing.T, f *fixture, id string) {
	t.Helper()
	if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error {
		var session domain.Session
		if err := tx.Get(f.ctx, "sessions", id, &session); err != nil {
			return err
		}
		session.Archived = true
		if err := tx.Save(f.ctx, "sessions", &session.Meta, &session); err != nil {
			return err
		}
		return PauseSession(f.ctx, tx, id)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRejectsEventsKeepsManagementAndDoesNotResumeBuffers(t *testing.T) {
	f := manualWorkerFixture(t, nil)
	ctx := context.Background()
	endpoint := f.create(t)
	other := f.session
	other.Meta = domain.Meta{ID: "session_other"}
	if err := f.store.Save(ctx, "sessions", &other.Meta, &other); err != nil {
		t.Fatal(err)
	}
	otherEndpoint, err := f.s.Create(f.ctx, f.agent.ID, CreateInput{Title: "Other target", SessionID: other.ID})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "accepted-before-archive", Text: "Do not replay this event"}
	if _, err = f.s.Accept(ctx, endpoint.ComputerID, endpoint.ID, event); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Accept(ctx, otherEndpoint.ComputerID, otherEndpoint.ID, Event{EventID: "other", Text: "Keep this event"}); err != nil {
		t.Fatal(err)
	}
	archiveTarget(t, f, f.session.ID)
	// Before the post-commit cache invalidation, both an old receipt and the
	// warm configuration cache must already respect the durable archive flag.
	if _, err = f.s.Accept(ctx, endpoint.ComputerID, endpoint.ID, event); !archiveConflict(err) {
		t.Fatalf("duplicate acceptance bypassed the archive: %v", err)
	}
	if _, err = f.s.Check(ctx, endpoint.ComputerID, endpoint.ID); !archiveConflict(err) {
		t.Fatalf("warm endpoint cache bypassed the archive: %v", err)
	}
	f.s.InvalidateSession(f.session.ID)
	got, err := f.s.Get(ctx, f.agent.ID, endpoint.ID)
	if err != nil || got.Enabled || got.Prompt != endpoint.Prompt || got.Notes != endpoint.Notes {
		t.Fatalf("archive discarded configuration or prevented reading: %+v %v", got, err)
	}
	notes := "/opt/example/new-listener.py"
	got, err = f.s.Update(ctx, f.agent.ID, endpoint.ID, Patch{Revision: got.Revision, Notes: &notes})
	if err != nil || got.Notes != notes {
		t.Fatalf("archived target prevented notes management: %+v %v", got, err)
	}
	enabled := true
	if _, err = f.s.Update(ctx, f.agent.ID, endpoint.ID, Patch{Revision: got.Revision, Enabled: &enabled}); !archiveConflict(err) {
		t.Fatalf("enabled endpoint targeting an archived Session: %v", err)
	}
	if _, err = f.s.Create(f.ctx, f.agent.ID, CreateInput{Title: "Archived target"}); !archiveConflict(err) {
		t.Fatalf("created endpoint targeting an archived Session: %v", err)
	}
	items, err := f.s.List(ctx, f.agent.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("archive hid retained endpoint configuration: %+v %v", items, err)
	}
	var receipt eventReceipt
	if err = f.store.Maintenance(ctx, receiptKind, eventKey(endpoint.ID, event.EventID), &receipt); err != nil {
		t.Fatalf("archive erased the accepted-event receipt: %v", err)
	}
	var persisted flow
	if err = f.store.Maintenance(ctx, flowKind, f.session.ID, &persisted); !domain.IsMissing(err) {
		t.Fatalf("archive retained its payload buffer: %+v %v", persisted, err)
	}
	var session domain.Session
	if err = f.store.Get(ctx, "sessions", f.session.ID, &session); err != nil {
		t.Fatal(err)
	}
	session.Archived = false
	if err = f.store.Save(ctx, "sessions", &session.Meta, &session); err != nil {
		t.Fatal(err)
	}
	workerClock(f, 2*MergeWindow)
	f.s.cycle(ctx)
	if len(f.delivered) != 1 || (<-f.delivered).SessionID != other.ID {
		t.Fatal("restore replayed an old buffer or stopped another target")
	}
	if _, err = f.s.Accept(ctx, endpoint.ComputerID, endpoint.ID, Event{EventID: "after-restore", Text: "Still paused"}); !archiveConflict(err) {
		t.Fatalf("restoration enabled the endpoint: %v", err)
	}
	got, err = f.s.Update(ctx, f.agent.ID, endpoint.ID, Patch{Revision: got.Revision, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Accept(ctx, endpoint.ComputerID, endpoint.ID, Event{EventID: "explicit-new", Text: "A new event after explicit enable"}); err != nil {
		t.Fatal(err)
	}
	workerClock(f, MergeWindow)
	f.s.cycle(ctx)
	if len(f.delivered) != 1 || (<-f.delivered).Events[0].EventID != "explicit-new" {
		t.Fatal("explicit enable did not admit only the new event")
	}
	archiveTarget(t, f, f.session.ID)
	f.s.InvalidateSession(f.session.ID)
	got, err = f.s.Get(ctx, f.agent.ID, endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Delete(ctx, f.agent.ID, endpoint.ID, got.Revision); err != nil {
		t.Fatalf("archived target prevented endpoint deletion: %v", err)
	}
}

func TestArchiveCommitPreventsReturningCallbackFromRecreatingDurableFlow(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		name := "retry"
		if accepted {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			f := manualWorkerFixture(t, func(context.Context, Batch) (DeliveryResult, error) {
				close(entered)
				<-release
				return DeliveryResult{Accepted: accepted, Handled: accepted}, nil
			})
			e := f.create(t)
			if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "callback", Text: "Old payload"}); err != nil {
				t.Fatal(err)
			}
			workerClock(f, MergeWindow)
			go func() { f.s.cycle(f.ctx); close(done) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("callback never started")
			}
			archiveTarget(t, f, f.session.ID)
			// Deliberately defer InvalidateSession: the returning callback must
			// obey the transaction itself, without depending on notification timing.
			close(release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("callback deadlocked against the archive transaction")
			}
			var row flow
			if err := f.store.Maintenance(f.ctx, flowKind, f.session.ID, &row); !domain.IsMissing(err) {
				t.Fatalf("callback recreated a durable archived flow: %+v %v", row, err)
			}
			f.s.InvalidateSession(f.session.ID)
			if delay := f.s.cycle(f.ctx); delay != time.Hour {
				t.Fatalf("archive retained an automatic retry: %s", delay)
			}
		})
	}
}

func TestArchiveBeforeDispatchCannotCallDeliveryOrRecoverAfterRestart(t *testing.T) {
	calls := 0
	f := manualWorkerFixture(t, func(context.Context, Batch) (DeliveryResult, error) { calls++; return DeliveryResult{}, nil })
	e := f.create(t)
	if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "queued", Text: "Never deliver"}); err != nil {
		t.Fatal(err)
	}
	archiveTarget(t, f, f.session.ID)
	workerClock(f, MergeWindow)
	if delay := f.s.cycle(f.ctx); calls != 0 || delay != time.Hour {
		t.Fatalf("archive dispatched its stale in-memory buffer: calls=%d delay=%s", calls, delay)
	}
	f.s = New(f.store, func(context.Context, Batch) (DeliveryResult, error) {
		t.Error("restart replayed archived payload")
		return DeliveryResult{}, nil
	}, nil)
	if err := f.s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "restart", Text: "Rejected"}); !archiveConflict(err) {
		t.Fatalf("restart accepted an archived target: %v", err)
	}
}

func archiveConflict(err error) bool {
	var problem *domain.Error
	return errors.As(err, &problem) && problem.Status == 409
}

func TestPauseSessionsDiscardsOnlySelectedTargetsBuffers(t *testing.T) {
	f := manualWorkerFixture(t, nil)
	ctx := context.Background()
	endpoints := []domain.WakeEndpoint{f.create(t)}
	for _, id := range []string{"second", "unrelated"} {
		session := f.session
		session.Meta = domain.Meta{ID: id}
		if err := f.store.Save(ctx, "sessions", &session.Meta, &session); err != nil {
			t.Fatal(err)
		}
		e, err := f.s.Create(f.ctx, f.agent.ID, CreateInput{Title: id, SessionID: id})
		if err != nil {
			t.Fatal(err)
		}
		endpoints = append(endpoints, e)
	}
	for _, e := range endpoints {
		if _, err := f.s.Accept(ctx, e.ComputerID, e.ID, Event{EventID: "preserve-receipt", Text: e.Title}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.Update(ctx, func(tx *persistence.Tx) error {
		return PauseSessions(ctx, tx, []string{f.session.ID, "second", f.session.ID})
	}); err != nil {
		t.Fatal(err)
	}
	for i, e := range endpoints {
		got, err := f.s.Get(ctx, f.agent.ID, e.ID)
		if err != nil || got.Enabled != (i == 2) {
			t.Fatalf("bulk pause affected wrong endpoint: %+v %v", got, err)
		}
		var state flow
		err = f.store.Maintenance(ctx, flowKind, e.SessionID, &state)
		if i < 2 && !domain.IsMissing(err) || i == 2 && err != nil {
			t.Fatalf("bulk pause cleared the wrong buffer: index=%d error=%v", i, err)
		}
		var receipt eventReceipt
		if err := f.store.Maintenance(ctx, receiptKind, eventKey(e.ID, "preserve-receipt"), &receipt); err != nil {
			t.Fatalf("bulk pause erased receipt history: %v", err)
		}
	}
	notifications := 0
	f.s.changed = func() { notifications++ }
	configuration, unsubscribe := f.s.SubscribeConfiguration()
	defer unsubscribe()
	f.s.InvalidateSessions([]string{f.session.ID, "second", f.session.ID})
	if notifications != 1 {
		t.Fatalf("bulk invalidation broadcast configuration %d times", notifications)
	}
	select {
	case <-configuration:
	default:
		t.Fatal("bulk invalidation did not notify the endpoint directory")
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	for i, e := range endpoints {
		_, cached := f.s.cache[e.ID]
		_, state := f.s.states[e.ID]
		_, flow := f.s.flows[e.SessionID]
		_, retry := f.s.retry[e.SessionID]
		_, dirty := f.s.dirty[e.SessionID]
		if i < 2 && (cached || state || flow || retry || dirty) || i == 2 && (!cached || !state || !flow || !dirty) {
			t.Fatalf("wrong volatile tree invalidation: index=%d cache=%v state=%v flow=%v retry=%v dirty=%v", i, cached, state, flow, retry, dirty)
		}
	}
}

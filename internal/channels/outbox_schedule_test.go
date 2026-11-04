package channels

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestOutboxDoesNotScanOrSendDuringUnrelatedReconcile(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return nil })
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	b := createBinding(t, p, s, c, "-100", "")
	queued, err := s.Enqueue(p.Context, p.Agent.ID, b.SessionID, SendRequest{IntentID: "only-an-outbox-event-wakes-this", Text: "Fixture reply."})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(p.Context)
	s.ctx = ctx
	defer func() { cancel(); s.workers.Wait() }()
	// Harness changes call reconcile. A burst must not dispatch the pending
	// send; the periodic deadline or explicit outbox signal owns that work.
	for range 20 {
		s.reconcile()
	}
	if bot.sends.Load() != 0 {
		t.Fatal("unrelated Harness reconciliation dispatched outbox work")
	}
	select {
	case <-s.outboxWake:
	default:
		t.Fatal("newly persisted send did not wake the outbox")
	}
	if !s.dispatchPending() {
		t.Fatal("pending work did not retain the periodic retry")
	}
	testkit.Await(t, p.Context, func() bool {
		var record domain.Outbox
		return p.Store.Get(p.Context, "outbox", queued[0].ID, &record) == nil && record.Status == "accepted"
	})
	if bot.sends.Load() != 1 {
		t.Fatalf("outbox sent %d times", bot.sends.Load())
	}
	testkit.Await(t, p.Context, func() bool {
		select {
		case <-s.outboxWake:
			return true
		default:
			return false
		}
	})
	if s.dispatchPending() {
		t.Fatal("confirmed history kept the idle periodic scan active")
	}
	// After the queue becomes idle, a later enqueue must still wake it without
	// relying on the periodic scan that has just been disabled.
	if _, err = s.Enqueue(p.Context, p.Agent.ID, b.SessionID, SendRequest{IntentID: "new-send-after-idle", Text: "Later fixture reply."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.outboxWake:
	default:
		t.Fatal("later enqueue did not wake the idle outbox")
	}
	if !s.dispatchPending() {
		t.Fatal("later pending work did not reactivate retries")
	}
	testkit.Await(t, p.Context, func() bool { return bot.sends.Load() == 2 })
}

func TestOutboxFailedDispatchDoesNotWakeItself(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return nil })
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	b := createBinding(t, p, s, c, "-100", "")
	queued, err := s.Enqueue(p.Context, p.Agent.ID, b.SessionID, SendRequest{IntentID: "temporary-adapter-error", Text: "Fixture reply."})
	if err != nil {
		t.Fatal(err)
	}
	<-s.outboxWake
	s.Registry[c.Adapter] = func(domain.Connection, string, *http.Client) (Adapter, error) {
		return nil, errors.New("fixture adapter is temporarily unavailable")
	}
	s.ctx = p.Context
	if !s.dispatchPending() {
		t.Fatal("pending adapter failure lost its periodic retry")
	}
	s.workers.Wait()
	select {
	case <-s.outboxWake:
		t.Fatal("failed dispatch scheduled an immediate retry")
	default:
	}
	var record domain.Outbox
	if err = p.Store.Get(p.Context, "outbox", queued[0].ID, &record); err != nil || record.Status != "pending" {
		t.Fatalf("failed pending send changed outcome: %s %v", record.Status, err)
	}
}

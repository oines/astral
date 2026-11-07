package channels

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
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

func TestOutboxTerminalPredecessorDoesNotKeepPeriodicRetryAlive(t *testing.T) {
	for _, terminal := range []string{"failed", "blocked", "unknown"} {
		t.Run(terminal, func(t *testing.T) {
			bot := newBot(t)
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
				t.Error("outbox scheduling called a model")
				return nil
			})
			s := New(p.Store, p.Harness)
			c := bot.connection(t, s, p.Agent.ID)
			binding := createBinding(t, p, s, c, "-100", "")
			s.ctx = p.Context
			defer s.workers.Wait()
			fragments, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, SendRequest{IntentID: "terminal-predecessor", Text: strings.Repeat("a", 5000)})
			if err != nil || len(fragments) != 2 {
				t.Fatalf("two-fragment fixture: count=%d err=%v", len(fragments), err)
			}
			<-s.outboxWake
			fragments[0].Status = terminal
			if err := p.Store.Save(p.Context, "outbox", &fragments[0].Meta, &fragments[0]); err != nil {
				t.Fatal(err)
			}
			readFragments := func() []domain.Outbox {
				t.Helper()
				result := make([]domain.Outbox, len(fragments))
				for i, fragment := range fragments {
					if err := p.Store.Get(p.Context, "outbox", fragment.ID, &result[i]); err != nil {
						t.Fatal(err)
					}
				}
				return result
			}
			before := readFragments()
			if s.dispatchPending() {
				t.Error("terminal-dependent pending fragment kept periodic retry alive")
			}
			s.workers.Wait()
			if bot.sends.Load() != 0 || !reflect.DeepEqual(before, readFragments()) {
				t.Fatal("terminal-dependent scan sent or changed the blocked fragments")
			}
			select {
			case <-s.outboxWake:
				t.Fatal("terminal-dependent scan woke itself")
			default:
			}
			// A new group in the same chat still owns a real enqueue wakeup and
			// must be delivered without reviving the terminal-dependent fragment.
			later, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, SendRequest{IntentID: "new-group-after-idle", Text: "Independent new message."})
			if err != nil || len(later) != 1 {
				t.Fatalf("independent enqueue: count=%d err=%v", len(later), err)
			}
			select {
			case <-s.outboxWake:
			default:
				t.Fatal("independent enqueue did not wake the idle outbox")
			}
			if !s.dispatchPending() {
				t.Error("independent pending group lost its periodic retry")
			}
			s.workers.Wait()
			var delivered domain.Outbox
			if err := p.Store.Get(p.Context, "outbox", later[0].ID, &delivered); err != nil || delivered.Status != "accepted" || bot.sends.Load() != 1 {
				t.Fatalf("independent group was not delivered exactly once: status=%s sends=%d err=%v", delivered.Status, bot.sends.Load(), err)
			}
			select {
			case <-s.outboxWake:
			default:
				t.Fatal("committed independent send did not wake its completion scan")
			}
			if s.dispatchPending() {
				t.Error("terminal-dependent fragment kept retries alive after independent completion")
			}
			s.workers.Wait()
			if bot.sends.Load() != 1 || !reflect.DeepEqual(before, readFragments()) {
				t.Fatal("idle scan changed or retried the terminal-dependent group")
			}
		})
	}
}

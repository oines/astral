package channels

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

func inboxRecoveryPosition(s *Service, bindingID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handoffs[bindingID]
}

func inboxRecoveryArchive(t *testing.T, p *testkit.Platform, b domain.ConversationBinding, id, trigger string, self bool) (domain.Observation, int64) {
	t.Helper()
	var conversation domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", b.ConversationID, &conversation); err != nil {
		t.Fatal(err)
	}
	event := domain.Observation{
		ID: id, ExternalEventID: id, SchemaVersion: 1, Kind: "message.created",
		ConnectionID: conversation.ConnectionID, ConversationID: b.ConversationID,
		MessageID: id, Text: "Recovery fixture " + id, Trigger: trigger, Self: self,
		Sender: domain.Sender{ID: "7", Name: "Ada"}, SentAt: time.Now(), ReceivedAt: time.Now(),
	}
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	sequence, err := p.Store.ObservationSequence(p.Context, id)
	if err != nil {
		t.Fatal(err)
	}
	return event, sequence
}

func TestInboxRecoveryYieldsAfterOnePageForNewDelivery(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("replaying already accepted direct inputs must not call the model")
		return testkit.Text("Unexpected recovery generation.")
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, false, false)
	session, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// Model a restart after Core accepted direct drivers, but before the Host
	// committed its recovery scan position. Replaying them must not create a
	// second driver or drain multiple archive pages in one inbox iteration.
	const historical = 205
	sequences := make([]int64, historical)
	for i := range historical {
		event, sequence := inboxRecoveryArchive(t, p, binding, fmt.Sprintf("accepted-before-restart-%03d", i), "mention", false)
		sequences[i] = sequence
		if _, err := session.Steer(p.Context, core.Input{ID: event.ID + ":trigger", Message: messages.Trigger(event)}); err != nil {
			t.Fatal(err)
		}
		if err := s.deliver(p.Context, binding, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.handoffDirectObservations(p.Context, binding, sequences[historical-1]); err != nil {
		t.Fatal(err)
	}
	if position := inboxRecoveryPosition(s, binding.ID); position != sequences[99] {
		t.Fatalf("one recovery call passed its first page: position=%d want=%d", position, sequences[99])
	}

	// The normal inbox loop delivers new archive entries before its next
	// recovery page. That delivery must remain possible while old handoffs lag.
	fresh, through := inboxRecoveryArchive(t, p, binding, "new-message-between-recovery-pages", "", false)
	if err := s.deliver(p.Context, binding, fresh); err != nil {
		t.Fatal(err)
	}
	if receipt, err := session.LookupInput(p.Context, fresh.ID); err != nil || receipt.State != core.InputApplied {
		t.Fatalf("new observation could not enter context between recovery pages: %+v %v", receipt, err)
	}
	if position := inboxRecoveryPosition(s, binding.ID); position != sequences[99] {
		t.Fatalf("new delivery incorrectly advanced historical handoffs: %d", position)
	}
	for _, want := range []int64{sequences[199], through} {
		if err := s.handoffDirectObservations(p.Context, binding, through); err != nil {
			t.Fatal(err)
		}
		if position := inboxRecoveryPosition(s, binding.ID); position != want {
			t.Fatalf("recovery did not advance by one bounded page: position=%d want=%d", position, want)
		}
	}
	if p.Requests.Load() != 0 || session.State().PendingInputs[core.InputSteer] != historical {
		t.Fatalf("recovery changed the accepted driver queue: requests=%d pending=%d", p.Requests.Load(), session.State().PendingInputs[core.InputSteer])
	}
	if _, err := session.LookupInput(p.Context, "accepted-before-restart-204:trigger:wakeup"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("recovery created a duplicate driving input: %v", err)
	}
}

func TestInboxRecoveryBusyRetainsProcessedPrefix(t *testing.T) {
	entered := make(chan struct{})
	gate, release := activeDeliveryGate(t)
	p := testkit.New(t, 0, func(ctx context.Context, call int, _ map[string]any) any {
		if call != 1 {
			t.Error("busy recovery unexpectedly started another model request")
			return testkit.Text("Unexpected recovery generation.")
		}
		close(entered)
		select {
		case <-gate:
			return testkit.Text("Finished the earlier request.")
		case <-ctx.Done():
			return nil
		}
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, false, false)
	if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "work-during-recovery", Text: "Hold the already frozen request."}); err != nil {
		t.Fatal(err)
	}
	activeDeliveryWait(t, p.Context, entered, "frozen request before new observations")
	session, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	covered, _ := inboxRecoveryArchive(t, p, binding, "already-accepted-direct-driver", "mention", false)
	if _, err := session.Steer(p.Context, core.Input{ID: covered.ID + ":trigger", Message: messages.Trigger(covered)}); err != nil {
		t.Fatal(err)
	}
	prefix, prefixSequence := inboxRecoveryArchive(t, p, binding, "passive-before-busy", "", false)
	busy, _ := inboxRecoveryArchive(t, p, binding, "unseen-direct-message", "mention", false)
	self, _ := inboxRecoveryArchive(t, p, binding, "self-after-busy", "mention", true)
	trailing, through := inboxRecoveryArchive(t, p, binding, "passive-after-busy", "", false)
	for _, event := range []domain.Observation{covered, prefix, busy, self, trailing} {
		if err := s.deliver(p.Context, binding, event); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := s.handoffDirectObservations(p.Context, binding, through); err != nil {
			t.Fatal(err)
		}
		if position := inboxRecoveryPosition(s, binding.ID); position != prefixSequence {
			t.Fatalf("busy recovery skipped an unseen observation or lost its processed prefix: position=%d want=%d", position, prefixSequence)
		}
	}
	if receipt, err := session.LookupInput(p.Context, busy.ID+":trigger"); err != nil || receipt.State != core.InputPending {
		t.Fatalf("busy observation was consumed before a later request: %+v %v", receipt, err)
	}
	if _, err := session.LookupInput(p.Context, busy.ID+":trigger:wakeup"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("busy recovery admitted another driver: %v", err)
	}
	if p.Requests.Load() != 1 {
		t.Fatalf("busy recovery started an additional model request: %d", p.Requests.Load())
	}
	if err := p.Harness.Stop(p.Context, binding.SessionID); err != nil {
		t.Fatal(err)
	}
	release()
}

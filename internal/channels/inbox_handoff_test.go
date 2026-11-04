package channels

import (
	"context"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

func TestDirectHandoffPositionSurvivesRestartAndChecksBindingIdentity(t *testing.T) {
	p, s, b := storageChat(t)
	through := b.StartSequence + 20
	if err := s.saveHandoffPosition(p.Context, b, through); err != nil {
		t.Fatal(err)
	}
	s = New(p.Store, p.Harness)
	if position := inboxRecoveryPosition(t, s, b); position != through {
		t.Fatalf("restart lost confirmed handoffs: %d", position)
	}
	if err := s.saveHandoffPosition(p.Context, b, through-1); err != nil {
		t.Fatal(err)
	}
	if position := inboxRecoveryPosition(t, s, b); position != through {
		t.Fatalf("older worker regressed handoff position: %d", position)
	}
	for _, change := range []func(*domain.ConversationBinding){
		func(b *domain.ConversationBinding) { b.AgentID += "other" },
		func(b *domain.ConversationBinding) { b.SessionID += "other" },
		func(b *domain.ConversationBinding) { b.ID += "other" },
		func(b *domain.ConversationBinding) { b.ConversationID += "other" },
		func(b *domain.ConversationBinding) { b.StartSequence++ },
	} {
		other := b
		change(&other)
		if position := inboxRecoveryPosition(t, s, other); position != other.StartSequence {
			t.Fatalf("handoff reused another binding's evidence: %d", position)
		}
	}
	stale := b
	stale.SessionID += "other"
	if err := s.saveHandoffPosition(p.Context, stale, through+1); err == nil {
		t.Fatal("stale binding writer changed confirmed handoffs")
	}
	if position := inboxRecoveryPosition(t, s, b); position != through {
		t.Fatalf("stale binding writer changed position: %d", position)
	}
	if err := p.Store.Update(p.Context, func(tx *persistence.Tx) error { return tx.PurgeSessionDeliveries(p.Context, b.SessionID) }); err != nil {
		t.Fatal(err)
	}
	if position := inboxRecoveryPosition(t, s, b); position != b.StartSequence {
		t.Fatalf("Session deletion retained handoff evidence: %d", position)
	}
}

func TestDirectHandoffDoesNotConfirmMerelyAcceptedWakeup(t *testing.T) {
	entered := make(chan struct{})
	gate, release := activeDeliveryGate(t)
	p := testkit.New(t, 0, func(ctx context.Context, call int, _ map[string]any) any {
		if call != 1 {
			t.Error("handoff started a second generation")
			return nil
		}
		close(entered)
		select {
		case <-gate:
			return testkit.Text("Handled the frozen observation.")
		case <-ctx.Done():
			return nil
		}
	})
	s := New(p.Store, p.Harness)
	b := activeDeliveryChat(t, p, s, false, false)
	event, through := inboxRecoveryArchive(t, p, b, "passive-direct-before-handoff", "mention", false)
	var connection domain.Connection
	var conversation domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", b.ConversationID, &conversation); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.Get(p.Context, "connections", conversation.ConnectionID, &connection); err != nil {
		t.Fatal(err)
	}
	session, err := p.Harness.Core(p.Context, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.InjectContext(p.Context, core.Input{ID: event.ID + ":trigger", Contexts: []core.Message{messages.Observation(event, connection, conversation, nil, "")}}); err != nil {
		t.Fatal(err)
	}
	if err = s.handoffDirectObservations(p.Context, b, through); err != nil {
		t.Fatal(err)
	}
	// Continue returned an accepted driver. Confirmation must await another
	// inspection of actual request/terminal evidence, not assume it was read.
	if position := inboxRecoveryPosition(t, New(p.Store, p.Harness), b); position != b.StartSequence {
		t.Fatalf("a merely accepted wakeup became durable completion: %d", position)
	}
	activeDeliveryWait(t, p.Context, entered, "frozen generation for accepted wakeup")
	if err = s.handoffDirectObservations(p.Context, b, through); err != nil {
		t.Fatal(err)
	}
	if position := inboxRecoveryPosition(t, New(p.Store, p.Harness), b); position != through {
		t.Fatalf("frozen request evidence was not persisted while the run remained active: %d", position)
	}
	if _, err = session.LookupInput(p.Context, event.ID+":trigger:wakeup"); err != nil {
		t.Fatalf("accepted wakeup identity disappeared: %v", err)
	}
	release()
	p.Idle(t, b.SessionID)
}

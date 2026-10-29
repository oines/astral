package channels

import (
	"context"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestUnknownReactionActorsStayInContextWithoutIndependentlyProbing(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	connection := newBot(t).connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, connection, "-100", "")
	var chat domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &chat); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProactive(p.Context, chat.ID, chat.Revision, true); err != nil {
		t.Fatal(err)
	}
	session, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(actor, sender string, self bool, kind string) domain.Observation {
		t.Helper()
		id := domain.ID("reaction")
		event := domain.Observation{SchemaVersion: 1, ID: id, ExternalEventID: id, ConnectionID: connection.ID, ConversationID: chat.ID, Kind: kind, MessageID: "101", Sender: domain.Sender{ID: sender}, Self: self, SentAt: time.Now(), ReceivedAt: time.Now(), Reaction: &domain.ReactionEvent{ActorType: actor, Reactions: []domain.Reaction{{Type: "emoji", Emoji: "👍"}}}}
		if _, err := s.archiveEvent(p.Context, event); err != nil {
			t.Fatal(err)
		}
		if err := s.deliver(p.Context, binding, event); err != nil {
			t.Fatal(err)
		}
		testkit.Await(t, p.Context, func() bool {
			receipt, err := session.LookupInput(p.Context, id)
			return err == nil && receipt.State == core.InputApplied
		})
		p.Idle(t, binding.SessionID)
		return event
	}
	for _, test := range []struct{ actor, sender, kind string }{
		{"unknown", "", "message.reaction.added"},
		{"user", "", "message.reaction.removed"},
		{"unknown", "", "message.reaction.counts_updated"},
	} {
		event := deliver(test.actor, test.sender, false, test.kind)
		sequence, err := p.Store.ObservationSequence(p.Context, event.ID)
		if err != nil {
			t.Fatal(err)
		}
		window, err := s.pendingProbe(p.Context, binding, sequence, time.Now().Add(time.Minute))
		if err != nil || window.Through != 0 || len(window.Inputs) != 0 {
			t.Fatalf("unknown actor became a participation candidate: %+v %v", window, err)
		}
		archived, err := p.Store.Observation(p.Context, event.ID)
		if err != nil || archived.Self {
			t.Fatal("unknown actor was removed or falsely labelled as Self")
		}
	}
	known := deliver("user", "7", false, "message.reaction.added")
	sequence, _ := p.Store.ObservationSequence(p.Context, known.ID)
	window, err := s.pendingProbe(p.Context, binding, sequence, time.Now().Add(time.Minute))
	if err != nil || window.Through != sequence || len(window.Inputs) != 1 || window.Inputs[0] != known.ID {
		t.Fatalf("a known external actor no longer follows proactive rules: %+v %v", window, err)
	}
	view, err := p.Harness.ReadContext(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	observations := 0
	for _, message := range view.Request.Messages {
		if message.ContextKind == "im.observation" {
			observations++
		}
	}
	if observations != 4 || p.Requests.Load() != 0 {
		t.Fatalf("passive reactions were dropped or woke a model: observations=%d requests=%d", observations, p.Requests.Load())
	}
}

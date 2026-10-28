package channels

import (
	"context"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestProbeReadContextPreservesRealDeliveredObservationInModelRequest(t *testing.T) {
	requests := make(chan map[string]any, 1)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		requests <- request
		return testkit.Tool("decide", map[string]bool{"participate": false})
	})
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, connection, "-100", "")
	var chat domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &chat); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProactive(p.Context, chat.ID, chat.Revision, true); err != nil {
		t.Fatal(err)
	}
	const text = "astral在吗？看到这条消息请回复。"
	event := domain.Observation{ID: "real-probe-context", SchemaVersion: 1, Kind: "message.created", ConnectionID: connection.ID, ConversationID: binding.ConversationID,
		MessageID: "1", Text: text, Sender: domain.Sender{ID: "7", Name: "Ada"}, SentAt: time.Now().Add(-time.Minute), ReceivedAt: time.Now().Add(-time.Minute)}
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	if err := s.deliver(p.Context, binding, event); err != nil {
		t.Fatal(err)
	}
	testkit.Await(t, p.Context, func() bool {
		receipt, err := p.Harness.Receipt(p.Context, binding.SessionID, event.ID)
		return err == nil && receipt.State == string(core.InputApplied)
	})
	through, err := p.Store.ObservationSequence(p.Context, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	window, err := s.pendingProbe(p.Context, binding, through, time.Now())
	if err != nil || window.Through != through || window.InputID != event.ID {
		t.Fatalf("the real delivered observation was not eligible for Probe: %+v %v", window, err)
	}
	view, err := p.Harness.ReadContext(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range view.Request.Messages {
		for _, part := range message.Content {
			if strings.Contains(part.Text, text) {
				found = true
				if message.Role != core.RoleUser || message.ContextKind != "im.observation" {
					t.Fatalf("ReadContext lost the observation discriminator: role=%s kind=%s", message.Role, message.ContextKind)
				}
			}
		}
	}
	if !found {
		t.Fatal("ReadContext omitted the actual applied IM observation")
	}
	if participate, err := s.shouldParticipate(p.Context, binding, view); err != nil || participate {
		t.Fatalf("unexpected local Probe result: %v %v", participate, err)
	}
	request := <-requests
	if !strings.Contains(string(encoded(request["messages"])), text) {
		t.Fatal("the actual Probe wire request filtered out its real delivered observation")
	}
	if p.Requests.Load() != 1 || bot.sends.Load() != 0 {
		t.Fatal("the observation check started extra model requests or sent an IM message")
	}
}

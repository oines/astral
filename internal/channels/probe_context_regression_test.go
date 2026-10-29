package channels

import (
	"context"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
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
	for _, title := range []string{"项目群", "改名后的项目群"} {
		if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &chat); err != nil {
			t.Fatal(err)
		}
		chat.Title = title
		if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
			t.Fatal(err)
		}
		// Reuse the same real observation: only the next request's current chat
		// metadata should change when the platform reports a new group name.
		if participate, err := s.shouldParticipate(p.Context, binding, view); err != nil || participate {
			t.Fatalf("unexpected local Probe result: %v %v", participate, err)
		}
		request := <-requests
		wireMessages := request["messages"].([]any)
		system := wireMessages[0].(map[string]any)
		if system["role"] != "system" || !strings.Contains(system["content"].(string), messages.ChatContext(connection, chat)) {
			t.Fatalf("Probe omitted the latest bound chat metadata: %s", encoded(system))
		}
		observations := string(encoded(wireMessages[1:]))
		if !strings.Contains(observations, text) || !strings.Contains(observations, "conversation-id=\\\""+chat.ID+"\\\"") {
			t.Fatal("the actual Probe wire request lost its real observation or compact conversation reference")
		}
		for _, field := range []string{"current-channel:", "connection-id:", "conversation-id:", "chat-type:", "chat-title:", "chat-id:"} {
			if strings.Contains(observations, field) {
				t.Fatalf("Probe observations repeated chat metadata %q: %s", field, observations)
			}
		}
	}
	if p.Requests.Load() != 2 || bot.sends.Load() != 0 {
		t.Fatal("the observation check started extra model requests or sent an IM message")
	}
}

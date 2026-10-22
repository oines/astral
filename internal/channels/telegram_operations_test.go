package channels

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestTelegramOperationsScopeEveryChatAndTopic(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	first := createBinding(t, p, s, c, "-100", "")
	topic := createBinding(t, p, s, c, "-200", "22")
	forbidden, err := s.ResolveConversation(p.Context, c.ID, "-300", "")
	if err != nil {
		t.Fatal(err)
	}
	b := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: first.SessionID}
	catalog, err := s.TelegramOperations(p.Context, p.Agent.ID, c.ID)
	if err != nil || len(catalog) < 90 {
		t.Fatalf("catalog is incomplete: %d %v", len(catalog), err)
	}
	for _, op := range catalog {
		if op.Name == "getUpdates" || op.Name == "setWebhook" {
			t.Fatal("platform identity operation escaped chat scope")
		}
	}
	invoke := func(method, conversationID, sourceID string, parameters map[string]any) error {
		_, err := s.TelegramOperation(p.Context, b, c.ID, method, TelegramRequest{ConversationID: conversationID, SourceConversationID: sourceID, Parameters: parameters})
		return err
	}
	for name, call := range map[string]func() error{
		"unscoped method": func() error { return invoke("getUpdates", first.ConversationID, "", nil) },
		"target override": func() error {
			return invoke("setChatTitle", first.ConversationID, "", map[string]any{"chat_id": "-300", "title": "bad"})
		},
		"out-of-scope source": func() error {
			return invoke("forwardMessage", first.ConversationID, forbidden.ID, map[string]any{"message_id": float64(1)})
		},
		"nested reply chat": func() error {
			return invoke("sendMessage", first.ConversationID, "", map[string]any{"text": "bad", "reply_parameters": map[string]any{"chat_id": "-300", "message_id": float64(1)}})
		},
		"sender chat": func() error {
			return invoke("banChatSenderChat", first.ConversationID, "", map[string]any{"sender_chat_id": "-300"})
		},
		"inline override": func() error {
			return invoke("editMessageText", first.ConversationID, "", map[string]any{"inline_message_id": "foreign-inline", "text": "bad"})
		},
		"command global scope": func() error {
			return invoke("setMyCommands", first.ConversationID, "", map[string]any{"scope": map[string]any{"type": "default"}, "commands": []any{}})
		},
		"topic-wide privilege": func() error { return invoke("setChatTitle", topic.ConversationID, "", map[string]any{"title": "bad"}) },
		"foreign topic message": func() error {
			return invoke("deleteMessage", topic.ConversationID, "", map[string]any{"message_id": float64(7)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("operation bypassed chat scope")
			}
		})
	}
	if len(bot.operations) != 0 || bot.sends.Load() != 0 {
		t.Fatal("unauthorized operation reached Telegram")
	}
	fact := domain.Observation{ID: "topic-message", ExternalEventID: "topic-message", ConnectionID: c.ID, ConversationID: topic.ConversationID, MessageID: "7", Kind: "message.created", Text: "source fact", ReceivedAt: time.Now(), SentAt: time.Now()}
	if _, err := p.Store.Archive(p.Context, fact); err != nil {
		t.Fatal(err)
	}
	if err := invoke("forwardMessage", first.ConversationID, topic.ConversationID, map[string]any{"message_id": float64(7)}); err != nil {
		t.Fatal(err)
	}
	if err := invoke("deleteMessage", topic.ConversationID, "", map[string]any{"message_id": float64(7)}); err != nil {
		t.Fatal(err)
	}
	if err := invoke("setMyCommands", first.ConversationID, "", map[string]any{"commands": []any{}}); err != nil {
		t.Fatal(err)
	}
	if len(bot.operations) != 3 {
		t.Fatalf("authorized operations=%v", bot.operations)
	}
	if bot.parameters[0]["from_chat_id"] != "-200" || bot.parameters[0]["chat_id"] != "-100" {
		t.Fatalf("wrong frozen chat scope: %v", bot.parameters[0])
	}
	if body := string(encoded(bot.parameters[2])); !strings.Contains(body, `"scope":{"chat_id":"-100","type":"chat"}`) {
		t.Fatalf("missing scoped commands: %s", body)
	}
	deleted, err := p.Store.Message(p.Context, topic.ConversationID, "7")
	if err != nil || deleted.Kind != "message.deleted" {
		t.Fatalf("delete result not archived: %+v %v", deleted, err)
	}
}

func TestAllModeCanResolveAndSendWithoutActivatingSession(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	c.AccessMode = "all"
	if err := s.SaveConnection(p.Context, &c, nil); err != nil {
		t.Fatal(err)
	}
	chat, err := s.ResolveForAgent(p.Context, p.Agent.ID, c.ID, "-100", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Enqueue(p.Context, p.Agent.ID, p.Session.ID, SendRequest{IntentID: "unactivated", ConversationID: chat.ID, Text: "explicit send"})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].BindingID != "" {
		t.Fatal("send created an inbound binding")
	}
	if err = s.dispatch(p.Context, out[0].ID); err != nil {
		t.Fatal(err)
	}
	bindings, _ := list[domain.ConversationBinding](p.Context, p.Store, "bindings")
	if len(bindings) != 0 {
		t.Fatal("API activity activated a Session")
	}
	visible, err := s.VisibleConnections(p.Context, p.Agent.ID)
	if err != nil || len(visible) != 1 || visible[0].CredentialID != "" || len(visible[0].Config) != 0 {
		t.Fatalf("account discovery exposed private config: %+v %v", visible, err)
	}
	if _, err = s.ResolveForAgent(p.Context, "other-agent", c.ID, "-100", ""); err == nil {
		t.Fatal("cross-Agent account resolved")
	}
	c.AccessMode = "specified"
	if err = s.SaveConnection(p.Context, &c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveForAgent(p.Context, p.Agent.ID, c.ID, "-100", ""); err == nil {
		t.Fatal("specified mode retained unactivated chat access")
	}
}

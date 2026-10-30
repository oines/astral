package channels

import (
	"context"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

func TestPrivateChatBindingsOverrideAllAndFollowCurrentBinding(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	account := bot.connection(t, s, p.Agent.ID)
	account.AccessMode = "all"
	if err := s.SaveConnection(p.Context, &account, nil); err != nil {
		t.Fatal(err)
	}
	chat, err := s.ResolveForAgent(p.Context, p.Agent.ID, account.ID, "-100", "")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := s.Bind(p.Context, chat.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	queuedRequest := SendRequest{IntentID: "queued-before-private", ConversationID: chat.ID, Text: "queued before privacy changed"}
	queued, err := s.Enqueue(p.Context, p.Agent.ID, p.Session.ID, queuedRequest)
	if err != nil || len(queued) != 1 {
		t.Fatalf("initial send intent: %+v %v", queued, err)
	}
	var session domain.Session
	if err := p.Store.Get(p.Context, "sessions", bound.SessionID, &session); err != nil {
		t.Fatal(err)
	}
	session.Private = true
	if err := p.Store.Save(p.Context, "sessions", &session.Meta, &session); err != nil {
		t.Fatal(err)
	}
	other := persistence.WithSessionScope(p.Context, p.Agent.ID, p.Session.ID)
	if err := s.AuthorizeConversation(other, p.Agent.ID, chat.ID); !domain.IsMissing(err) {
		t.Fatalf("all mode bypassed private chat: %v", err)
	}
	if chats, err := s.VisibleConversations(other, p.Agent.ID); err != nil || len(chats) != 0 {
		t.Fatalf("private chat leaked through list: %+v %v", chats, err)
	}
	if _, err := s.ResolveForAgent(other, p.Agent.ID, account.ID, "-100", ""); !domain.IsMissing(err) {
		t.Fatalf("resolve bypassed private chat: %v", err)
	}
	if _, _, err := s.AttachmentContent(other, domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}, chat.ID, "known-message", "known-file"); !domain.IsMissing(err) {
		t.Fatalf("attachment read bypassed private chat: %v", err)
	}
	if _, err := s.Enqueue(other, p.Agent.ID, p.Session.ID, SendRequest{IntentID: "private-send", ConversationID: chat.ID, Text: "must not send"}); !domain.IsMissing(err) {
		t.Fatalf("send bypassed private chat: %v", err)
	}
	if _, err := s.Enqueue(other, p.Agent.ID, p.Session.ID, queuedRequest); !domain.IsMissing(err) {
		t.Fatalf("cached receipt bypassed current chat privacy: %v", err)
	}
	if err := s.dispatch(p.Context, queued[0].ID); err != nil {
		t.Fatal(err)
	}
	var blocked domain.Outbox
	if err := p.Store.Get(p.Context, "outbox", queued[0].ID, &blocked); err != nil || blocked.Status != "blocked" || bot.sends.Load() != 0 {
		t.Fatalf("queued intent bypassed later privacy: %+v %v", blocked, err)
	}
	own := persistence.WithSessionScope(p.Context, p.Agent.ID, session.ID)
	if err := s.AuthorizeConversation(own, p.Agent.ID, chat.ID); err != nil {
		t.Fatalf("private Session lost its own chat: %v", err)
	}
	if err := s.AuthorizeConversation(p.Context, p.Agent.ID, chat.ID); err != nil {
		t.Fatalf("Owner lost access to private chat: %v", err)
	}
	// A later public binding is the entire privacy source of truth; no sticky
	// private-chat state survives the previous relationship.
	if _, err := s.Bind(p.Context, chat.ID, p.Session.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeConversation(other, p.Agent.ID, chat.ID); err != nil {
		t.Fatalf("rebound public chat retained stale privacy: %v", err)
	}
}

func TestTelegramParentChatCannotOperateOnPrivateTopicMessages(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	account := bot.connection(t, s, p.Agent.ID)
	account.AccessMode = "all"
	if err := s.SaveConnection(p.Context, &account, nil); err != nil {
		t.Fatal(err)
	}
	parent := createBinding(t, p, s, account, "-100", "")
	topic := createBinding(t, p, s, account, "-100", "77")
	var secret domain.Session
	if err := p.Store.Get(p.Context, "sessions", topic.SessionID, &secret); err != nil {
		t.Fatal(err)
	}
	secret.Private = true
	if err := p.Store.Save(p.Context, "sessions", &secret.Meta, &secret); err != nil {
		t.Fatal(err)
	}
	event := domain.Observation{ID: "private-topic-message", ExternalEventID: "private-topic-message", ConnectionID: account.ID, ConversationID: topic.ConversationID, MessageID: "123", Kind: "message.created", Text: "private topic content", ReceivedAt: time.Now(), SentAt: time.Now()}
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	caller := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: parent.SessionID}
	ctx := persistence.WithSessionScope(p.Context, caller.AgentID, caller.SessionID)
	for _, test := range []struct {
		method, source string
		parameters     map[string]any
	}{
		{"deleteMessage", "", map[string]any{"message_id": float64(123)}},
		{"setMessageReaction", "", map[string]any{"message_id": float64(123), "reaction": []any{}}},
		{"forwardMessage", parent.ConversationID, map[string]any{"message_id": float64(123)}},
		{"sendMessage", "", map[string]any{"text": "reply", "reply_parameters": map[string]any{"message_id": float64(123)}}},
	} {
		if _, err := s.TelegramOperation(ctx, caller, account.ID, test.method, TelegramRequest{ConversationID: parent.ConversationID, SourceConversationID: test.source, Parameters: test.parameters}); !domain.IsMissing(err) {
			t.Fatalf("%s bypassed private Topic through parent chat: %v", test.method, err)
		}
	}
	if _, err := s.Enqueue(ctx, caller.AgentID, caller.SessionID, SendRequest{IntentID: "parent-reply", ConversationID: parent.ConversationID, Text: "reply", ReplyTo: "123"}); !domain.IsMissing(err) {
		t.Fatalf("normal reply bypassed private Topic: %v", err)
	}
	if len(bot.operations) != 0 || bot.sends.Load() != 0 {
		t.Fatal("private Topic operation reached Telegram")
	}
}

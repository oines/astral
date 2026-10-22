package channels

import (
	"context"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func feishuServiceFixture(t *testing.T) (*Service, *testkit.Platform, *feishuFixture, domain.Connection) {
	t.Helper()
	platform := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	bot := newFeishuFixture(t)
	service := New(platform.Store, platform.Harness)
	connection := domain.Connection{Name: "飞书测试 Bot", AgentID: platform.Agent.ID, Adapter: "feishu.bot", Enabled: true,
		Config: encoded(FeishuConfig{AppID: "cli_app", BaseURL: bot.server.URL})}
	secret := "app-secret"
	if err := service.SaveConnection(platform.Context, &connection, &secret); err != nil {
		t.Fatal(err)
	}
	return service, platform, bot, connection
}

func TestAccountOperationsSpecifiedScopeAndAllAccessDoNotCreateSessions(t *testing.T) {
	s, p, _, connection := feishuServiceFixture(t)
	chat, err := s.ResolveConversation(p.Context, connection.ID, "oc_chat", "")
	if err != nil {
		t.Fatal(err)
	}
	sessionsBefore, err := p.Store.List(p.Context, "sessions")
	if err != nil {
		t.Fatal(err)
	}
	invoke := func() error {
		_, err := s.AccountOperation(p.Context, domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}, connection.ID, "get_chat", ChatOperationRequest{ConversationID: chat.ID})
		return err
	}
	catalog, err := s.AccountOperations(p.Context, p.Agent.ID, connection.ID)
	if err != nil || len(catalog) != 7 {
		t.Fatalf("Feishu operations catalog: %#v, %v", catalog, err)
	}
	if err = invoke(); err == nil {
		t.Fatal("specified access admitted an unbound chat")
	}
	connection.AccessMode = "all"
	if err = s.SaveConnection(p.Context, &connection, nil); err != nil {
		t.Fatal(err)
	}
	if err = invoke(); err != nil {
		t.Fatalf("all access rejected an unbound chat: %v", err)
	}
	sessionsAfter, err := p.Store.List(p.Context, "sessions")
	if err != nil || len(sessionsAfter) != len(sessionsBefore) {
		t.Fatalf("API access created a Session: before=%d after=%d err=%v", len(sessionsBefore), len(sessionsAfter), err)
	}
	bindings, err := p.Store.List(p.Context, "bindings")
	if err != nil || len(bindings) != 0 {
		t.Fatalf("API access activated inbound delivery: %#v, %v", bindings, err)
	}
	connection.AccessMode = "specified"
	if err = s.SaveConnection(p.Context, &connection, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Bind(p.Context, chat.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err = invoke(); err != nil {
		t.Fatalf("specified access rejected an explicitly bound chat: %v", err)
	}
}

func TestAccountOperationsRejectForeignAgentAccountAndMessageTargets(t *testing.T) {
	s, p, bot, connection := feishuServiceFixture(t)
	connection.AccessMode = "all"
	if err := s.SaveConnection(p.Context, &connection, nil); err != nil {
		t.Fatal(err)
	}
	chat, err := s.ResolveConversation(p.Context, connection.ID, "oc_chat", "")
	if err != nil {
		t.Fatal(err)
	}
	// These fixtures represent already verified additional accounts. Saving the
	// records directly isolates authorization from credential validation.
	other := connection
	other.Meta, other.AccountID = domain.Meta{ID: "connection_other"}, "ou_other"
	if err = p.Store.Save(p.Context, "connections", &other.Meta, &other); err != nil {
		t.Fatal(err)
	}
	otherChat := domain.Conversation{Meta: domain.Meta{ID: "conversation_other"}, ConnectionID: other.ID, ExternalID: "oc_other", Kind: "group", Title: "其他账号的群"}
	if err = p.Store.Save(p.Context, "conversations", &otherChat.Meta, &otherChat); err != nil {
		t.Fatal(err)
	}
	binding := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}
	invoke := func(b domain.ExecutionBinding, accountID, name, conversationID string, parameters map[string]any) error {
		_, err := s.AccountOperation(p.Context, b, accountID, name, ChatOperationRequest{ConversationID: conversationID, Parameters: parameters})
		return err
	}
	foreign := domain.ExecutionBinding{AgentID: "agent_foreign", SessionID: "session_foreign"}
	if _, err = s.AccountOperations(p.Context, foreign.AgentID, connection.ID); err == nil {
		t.Fatal("catalog exposed another Bot's account")
	}
	for name, call := range map[string]func() error{
		"foreign agent":   func() error { return invoke(foreign, connection.ID, "get_chat", chat.ID, nil) },
		"foreign account": func() error { return invoke(binding, connection.ID, "get_chat", otherChat.ID, nil) },
		"chat ID override": func() error {
			return invoke(binding, connection.ID, "get_chat", chat.ID, map[string]any{"chat_id": "oc_other"})
		},
		"URL override": func() error {
			return invoke(binding, connection.ID, "get_chat", chat.ID, map[string]any{"url": bot.server.URL + "/open-apis/im/v1/chats/oc_other"})
		},
		"message outside archive": func() error {
			return invoke(binding, connection.ID, "get_message", chat.ID, map[string]any{"message_id": "om_parent"})
		},
		"unknown operation": func() error { return invoke(binding, connection.ID, "auth_token", chat.ID, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("operation escaped the selected Bot account and chat scope")
			}
		})
	}
	archive := func(conversationID, messageID string) {
		t.Helper()
		event := domain.Observation{ID: domain.ID("event"), SchemaVersion: 1, ExternalEventID: domain.ID("external"), ConnectionID: connection.ID, ConversationID: conversationID,
			MessageID: messageID, Kind: "message.created", Text: "archived fact", SentAt: time.Now().UTC(), ReceivedAt: time.Now().UTC()}
		if _, err := p.Store.Archive(p.Context, event); err != nil {
			t.Fatal(err)
		}
	}
	archive(otherChat.ID, "om_other")
	if err = invoke(binding, connection.ID, "delete_message", chat.ID, map[string]any{"message_id": "om_other"}); err == nil {
		t.Fatal("a message archived in a different chat could be deleted")
	}
	// Defense in depth: even an incorrectly associated local observation must
	// not let Feishu's global message endpoint escape the selected chat.
	archive(chat.ID, "om_other")
	if err = invoke(binding, connection.ID, "delete_message", chat.ID, map[string]any{"message_id": "om_other"}); err == nil {
		t.Fatal("a mismatching remote chat could be deleted")
	}
	if bot.deleted.Load() != 0 {
		t.Fatal("a rejected delete reached Feishu")
	}
	archive(chat.ID, "om_parent")
	if err = invoke(binding, connection.ID, "delete_message", chat.ID, map[string]any{"message_id": "om_parent"}); err != nil {
		t.Fatal(err)
	}
	deleted, err := p.Store.Message(p.Context, chat.ID, "om_parent")
	if err != nil || deleted.Kind != "message.deleted" || bot.deleted.Load() != 1 {
		t.Fatalf("successful platform delete was not archived: %#v, %v", deleted, err)
	}
}

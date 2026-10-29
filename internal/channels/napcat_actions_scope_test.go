package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestNapCatActionsEnforceBotAccountChatAndMessageAuthorization(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	var mutations, membershipQueries atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_msg":
			var body struct {
				MessageID string `json:"message_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			groupID := 456
			if body.MessageID == "-43" {
				groupID = 789 // Stale local short ID now resolves to another group.
			}
			fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":{"message_id":%s,"message_type":"group","group_id":%d,"user_id":123}}`, body.MessageID, groupID)
		case "/get_group_member_info":
			membershipQueries.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["group_id"] != "456" || body["user_id"] != "123" {
				t.Errorf("membership query escaped allowed chat: %+v", body)
			}
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"group_id":456,"user_id":123}}`)
		case "/set_msg_emoji_like", "/send_poke":
			mutations.Add(1)
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":null}`)
		default:
			t.Errorf("unexpected platform call: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s := New(p.Store, p.Harness)
	connection := domain.Connection{Meta: domain.Meta{ID: "napcat-account"}, AgentID: p.Agent.ID, Adapter: "qq.napcat", Platform: "qq", AccountID: "999", Enabled: true, AccessMode: "all",
		Config: encoded(NapCatConfig{BaseURL: server.URL, WebSocketURL: strings.Replace(server.URL, "http:", "ws:", 1) + "/ws"})}
	// Persist already verified records to isolate action authorization from login.
	if err := p.Store.Save(p.Context, "connections", &connection.Meta, &connection); err != nil {
		t.Fatal(err)
	}
	chat := domain.Conversation{Meta: domain.Meta{ID: "allowed-group"}, ConnectionID: connection.ID, ExternalID: "group:456", Kind: "group", Title: "Allowed"}
	otherChat := domain.Conversation{Meta: domain.Meta{ID: "other-group"}, ConnectionID: connection.ID, ExternalID: "group:789", Kind: "group", Title: "Other"}
	otherAccount := connection
	otherAccount.Meta, otherAccount.AccountID = domain.Meta{ID: "other-account"}, "888"
	if err := p.Store.Save(p.Context, "connections", &otherAccount.Meta, &otherAccount); err != nil {
		t.Fatal(err)
	}
	foreignChat := domain.Conversation{Meta: domain.Meta{ID: "other-account-group"}, ConnectionID: otherAccount.ID, ExternalID: "group:456", Kind: "group", Title: "Other account"}
	for _, c := range []*domain.Conversation{&chat, &otherChat, &foreignChat} {
		if err := p.Store.Save(p.Context, "conversations", &c.Meta, c); err != nil {
			t.Fatal(err)
		}
	}
	archive := func(conversationID, messageID string) {
		t.Helper()
		event := domain.Observation{SchemaVersion: 1, ID: domain.ID("event"), ConnectionID: connection.ID, ConversationID: conversationID, ExternalEventID: domain.ID("external"), Kind: "message.created", MessageID: messageID, Text: "archived", SentAt: time.Now().UTC(), ReceivedAt: time.Now().UTC()}
		if _, err := p.Store.Archive(p.Context, event); err != nil {
			t.Fatal(err)
		}
	}
	archive(chat.ID, "-42")
	archive(otherChat.ID, "-44")
	archive(chat.ID, "-43")
	binding := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}
	invoke := func(b domain.ExecutionBinding, name, conversationID string, params map[string]any) error {
		_, err := s.AccountOperation(p.Context, b, connection.ID, name, ChatOperationRequest{ConversationID: conversationID, Parameters: params})
		return err
	}
	foreign := domain.ExecutionBinding{AgentID: "other-agent", SessionID: "other-session"}
	if _, err := s.AccountOperations(p.Context, foreign.AgentID, connection.ID); err == nil {
		t.Fatal("catalog exposed another Bot's account")
	}
	for _, action := range []string{"poke", "set_reaction"} {
		params := map[string]any{"user_id": "123"}
		if action == "set_reaction" {
			params = map[string]any{"message_id": "-42", "emoji_id": "76"}
		}
		for _, b := range []domain.ExecutionBinding{foreign, binding} {
			conversationID := chat.ID
			if b.AgentID == binding.AgentID {
				conversationID = foreignChat.ID
			}
			if err := invoke(b, action, conversationID, params); err == nil {
				t.Fatalf("%s escaped Bot/account authorization", action)
			}
		}
		connection.AccessMode = "specified"
		if err := p.Store.Save(p.Context, "connections", &connection.Meta, &connection); err != nil {
			t.Fatal(err)
		}
		if err := invoke(binding, action, chat.ID, params); err == nil {
			t.Fatalf("%s reached an unallowed conversation", action)
		}
		connection.AccessMode = "all"
		if err := p.Store.Save(p.Context, "connections", &connection.Meta, &connection); err != nil {
			t.Fatal(err)
		}
	}
	for _, messageID := range []string{"-44", "-43", "-99"} {
		if err := invoke(binding, "set_reaction", chat.ID, map[string]any{"message_id": messageID, "emoji_id": "76"}); err == nil {
			t.Fatalf("reaction accepted an unarchived, cross-chat or remotely remapped message: %s", messageID)
		}
	}
	for _, params := range []map[string]any{
		{"user_id": "123", "group_id": "789"},
		{"user_id": "123", "target_id": "789"},
		{"user_id": "123", "url": server.URL},
	} {
		if err := invoke(binding, "poke", chat.ID, params); err == nil {
			t.Fatal("poke accepted alternate routing")
		}
	}
	if mutations.Load() != 0 || membershipQueries.Load() != 0 {
		t.Fatal("rejected actions reached the platform mutation or membership endpoint")
	}
	if err := invoke(binding, "set_reaction", chat.ID, map[string]any{"message_id": "-42", "emoji_id": "76"}); err != nil {
		t.Fatal(err)
	}
	if err := invoke(binding, "poke", chat.ID, map[string]any{"user_id": "123"}); err != nil {
		t.Fatal(err)
	}
	if mutations.Load() != 2 || membershipQueries.Load() != 1 {
		t.Fatalf("allowed actions did not execute once: mutations=%d membership=%d", mutations.Load(), membershipQueries.Load())
	}
}

func TestNapCatPokeRejectsUnprovenGroupMember(t *testing.T) {
	for _, member := range []string{
		`{"group_id":789,"user_id":123}`,
		`{"group_id":456,"user_id":789}`,
		`{}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/get_group_member_info" {
				t.Error("unproven group member reached send_poke")
			}
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":`+member+`}`)
		}))
		n := napcatTestAdapter(t, server)
		if _, err := napcatOperation(context.Background(), n, "poke", domain.Conversation{ExternalID: "group:456"}, map[string]any{"user_id": "123"}); err == nil {
			t.Fatal("poke accepted a missing or mismatching group member")
		}
		server.Close()
	}
}

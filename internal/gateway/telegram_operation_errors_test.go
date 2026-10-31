package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/testkit"
)

func TestTelegramOperationHTTPReturnsSafeRejectionAndFailureDetails(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	const token = "telegram-operation-test-token"
	var responseCode atomic.Int64
	var privateReason atomic.Bool
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/bot"+token+"/")
		switch method {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"Astral","username":"astral_bot"}}`)
		case "getWebhookInfo":
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case "setChatTitle", "getChatMember":
			calls.Add(1)
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if r.Method != "POST" || payload["chat_id"] != "-100" {
				t.Errorf("operation escaped selected chat: %s %+v", r.Method, payload)
			}
			description := "missing administrator rights"
			if privateReason.Load() {
				description += " " + token
			}
			w.WriteHeader(int(responseCode.Load()))
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": responseCode.Load(), "description": description})
		default:
			t.Errorf("unexpected Telegram method: %s", method)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	channel := channels.New(p.Store, p.Harness)
	account := domain.Connection{AgentID: p.Agent.ID, Name: "Telegram operation fixture", Adapter: "telegram.bot", AccessMode: "all", Enabled: true,
		Config: testkit.JSON(channels.TelegramConfig{BaseURL: server.URL})}
	credential := token
	if err := channel.SaveConnection(p.Context, &account, &credential); err != nil {
		t.Fatal(err)
	}
	chat := domain.Conversation{Meta: domain.Meta{ID: domain.ID("conversation")}, ConnectionID: account.ID, ExternalID: "-100", Kind: "group"}
	if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
		t.Fatal(err)
	}
	api := New(p.Harness, channel)
	p.Hub.Gateway = api.Handle
	source, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"operations", "telegram"} {
		for _, test := range []struct {
			name, method, code string
			upstream           int64
			private            bool
		}{
			{"explicit rejection", "setChatTitle", "platform_rejected", 400, false},
			{"redacted token", "setChatTitle", "platform_rejected", 403, true},
			{"mutation upstream failure", "setChatTitle", "outcome_unknown", 500, false},
			{"read upstream failure", "getChatMember", "platform_failed", 500, false},
		} {
			t.Run(route+"/"+test.name, func(t *testing.T) {
				responseCode.Store(test.upstream)
				privateReason.Store(test.private)
				before := calls.Load()
				parameters := map[string]any{"title": "New title"}
				if test.method == "getChatMember" {
					parameters = map[string]any{"user_id": "42"}
				}
				body := testkit.JSON(channels.ChatOperationRequest{ConversationID: chat.ID, Parameters: parameters})
				path := "/v1/accounts/" + account.ID + "/" + route + "/" + test.method
				reply, err := api.Handle(p.Context, computers.GatewayRequest{Binding: source, Method: "POST", Path: path, Body: body})
				if err != nil {
					t.Fatal(err)
				}
				want := fmt.Sprintf("Telegram %s failed (%d): missing administrator rights", test.method, test.upstream)
				if test.private {
					want += " [redacted]"
				}
				var problem httpcontract.Error
				if reply.Status != http.StatusBadGateway || json.Unmarshal(reply.Body, &problem) != nil || problem.Code != test.code || problem.Message != want {
					t.Fatalf("Gateway lost Telegram rejection details: HTTP %d %s", reply.Status, reply.Body)
				}
				if strings.Contains(string(reply.Body), token) {
					t.Fatalf("Gateway exposed Telegram credentials: %s", reply.Body)
				}
				if calls.Load()-before != 1 {
					t.Fatalf("operation automatically retried: %d attempts", calls.Load()-before)
				}
			})
		}
		before := calls.Load()
		bad := testkit.JSON(channels.ChatOperationRequest{ConversationID: chat.ID, Parameters: map[string]any{"title": "Title", "group_id": "-200"}})
		reply, err := api.Handle(p.Context, computers.GatewayRequest{Binding: source, Method: "POST", Path: "/v1/accounts/" + account.ID + "/" + route + "/setChatTitle", Body: bad})
		var problem httpcontract.Error
		if err != nil || reply.Status != http.StatusBadRequest || json.Unmarshal(reply.Body, &problem) != nil || problem.Code != "invalid" || !strings.Contains(problem.Message, "group_id") || calls.Load() != before {
			t.Fatalf("Telegram mapping swallowed validation: HTTP %d %s %v", reply.Status, reply.Body, err)
		}
	}
}

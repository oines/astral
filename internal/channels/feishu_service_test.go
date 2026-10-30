package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestFeishuServiceActivatesAndContinuesOnlyThroughExplicitSend(t *testing.T) {
	for _, chatType := range []string{"group", "p2p"} {
		t.Run(chatType, func(t *testing.T) {
			p := testkit.New(t, 1, func(_ context.Context, n int, request map[string]any) any {
				messages := string(encoded(request["messages"]))
				if n == 1 {
					if !strings.Contains(messages, "first Feishu request") || strings.Contains(messages, "before activation") {
						t.Error("first Feishu execution lost its trigger or replayed history before activation")
					}
				}
				if n == 3 {
					if !strings.Contains(messages, "first Feishu request") || !strings.Contains(messages, "second Feishu request") {
						t.Error("Feishu continuation lost the sustained Session context")
					}
					if chatType == "group" && !strings.Contains(messages, "passive context between requests") {
						t.Error("bound Feishu group lost passive context")
					}
				}
				if n == 1 || n == 3 {
					arguments := map[string]any{"text": fmt.Sprintf("explicit Feishu reply %d", (n+1)/2)}
					if n == 3 {
						arguments["reply_to"] = "om_second"
					}
					return testkit.Tool("send_message", arguments)
				}
				return testkit.Text("This assistant completion remains in Astral.")
			})
			bot := newFeishuFixture(t)
			type outbound struct {
				text, kind, path, target string
			}
			var outboundMu sync.Mutex
			var sends []outbound
			originalHandler := bot.server.Config.Handler
			// Preserve the adapter fixture's official protobuf websocket and HTTP
			// identity/resource routes, but issue distinct receipts for real sends.
			bot.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/chats/") {
					mode := chatType
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"name": "Feishu integration", "chat_mode": mode}})
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/open-apis/im/v1/messages" && !strings.HasSuffix(r.URL.Path, "/reply") {
					originalHandler.ServeHTTP(w, r)
					return
				}
				if r.Header.Get("Authorization") != "Bearer tenant-token" {
					t.Error("outbound Feishu message lost its authenticated account")
				}
				var body struct {
					Kind    string `json:"msg_type"`
					Target  string `json:"receive_id"`
					Content string `json:"content"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				var content struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
					t.Error(err)
					return
				}
				outboundMu.Lock()
				sends = append(sends, outbound{text: content.Text, kind: body.Kind, path: r.URL.Path, target: body.Target})
				n := len(sends)
				outboundMu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"message_id": fmt.Sprintf("om_out_%d", n)}})
			})
			service := New(p.Store, p.Harness)
			connection := domain.Connection{Name: "Feishu integration", AgentID: p.Agent.ID, Adapter: "feishu.bot", Enabled: true, AccessMode: "all",
				Config: encoded(FeishuConfig{AppID: "cli_app", BaseURL: bot.server.URL})}
			secret := "app-secret"
			if err := service.SaveConnection(p.Context, &connection, &secret); err != nil {
				t.Fatal(err)
			}
			chat, err := service.ResolveForAgent(p.Context, p.Agent.ID, connection.ID, "oc_chat", "")
			if err != nil {
				t.Fatal(err)
			}
			if bindings, _ := list[domain.ConversationBinding](p.Context, p.Store, "bindings"); len(bindings) != 0 {
				t.Fatal("Feishu API resolution created an inbound Session binding")
			}
			startChannels(t, service, p.Context)
			push := func(eventID, messageID, text string, mentioned, self bool) {
				bot.events <- feishuServiceEvent(eventID, messageID, chatType, text, mentioned, self)
			}
			if chatType == "group" {
				push("event_before", "om_before", "before activation", false, false)
				testkit.Await(t, p.Context, func() bool { _, err := p.Store.Message(p.Context, chat.ID, "om_before"); return err == nil })
				if bindings, _ := list[domain.ConversationBinding](p.Context, p.Store, "bindings"); len(bindings) != 0 || p.Requests.Load() != 0 {
					t.Fatal("ordinary Feishu group message activated a Session or model")
				}
			}
			push("event_first", "om_first", "first Feishu request", chatType == "group", false)
			var binding domain.ConversationBinding
			testkit.Await(t, p.Context, func() bool {
				binding, _ = service.activeBinding(p.Context, chat.ID)
				return binding.ID != ""
			})
			waitReceipts := func(count int) []domain.Outbox {
				t.Helper()
				var records []domain.Outbox
				testkit.Await(t, p.Context, func() bool {
					records, _ = list[domain.Outbox](p.Context, p.Store, "outbox")
					if len(records) != count {
						return false
					}
					for _, record := range records {
						if record.Status != "accepted" || len(record.PlatformMessageIDs) != 1 {
							return false
						}
					}
					return true
				})
				return records
			}
			waitReceipts(1)
			p.Idle(t, binding.SessionID)
			// A platform self echo must converge with the accepted Outbox fact
			// without triggering the model or creating a second Session.
			push("event_echo", "om_out_1", "explicit Feishu reply 1", false, true)
			if chatType == "group" {
				push("event_between", "om_between", "passive context between requests", false, false)
				testkit.Await(t, p.Context, func() bool { _, err := p.Store.Message(p.Context, chat.ID, "om_between"); return err == nil })
			}
			push("event_second", "om_second", "second Feishu request", chatType == "group", false)
			records := waitReceipts(2)
			p.Idle(t, binding.SessionID)
			current, _ := service.activeBinding(p.Context, chat.ID)
			if current.ID != binding.ID || current.SessionID != binding.SessionID || p.Requests.Load() != 4 {
				t.Fatalf("Feishu did not continue the same Session: initial=%#v current=%#v requests=%d", binding, current, p.Requests.Load())
			}
			for _, record := range records {
				if record.ConnectionID != connection.ID || record.ExternalID != "oc_chat" || record.ConversationID != chat.ID || record.BindingID != binding.ID || record.SessionID != binding.SessionID {
					t.Fatalf("Outbox lost its durable Feishu target snapshot: %#v", record)
				}
				fact, err := p.Store.Message(p.Context, chat.ID, record.PlatformMessageIDs[0])
				if err != nil || !fact.Self || fact.Text != archiveMessageText(record) {
					t.Fatalf("platform receipt was not archived as the accepted send: %#v, %v", fact, err)
				}
			}
			outboundMu.Lock()
			defer outboundMu.Unlock()
			if len(sends) != 2 {
				t.Fatalf("assistant completion or self echo caused an extra external send: %#v", sends)
			}
			for index, send := range sends {
				path, target := "/open-apis/im/v1/messages", "oc_chat"
				if index == 1 {
					path, target = "/open-apis/im/v1/messages/om_second/reply", ""
				}
				if send.text != fmt.Sprintf("explicit Feishu reply %d", index+1) || send.kind != "text" || send.path != path || send.target != target {
					t.Fatalf("send adapter lost reply targeting or exposed Web-only text: %#v", send)
				}
			}
		})
	}
}

func feishuServiceEvent(eventID, messageID, chatType, text string, mentioned, self bool) []byte {
	senderID, senderType := "ou_user", "user"
	if self {
		senderID, senderType = "ou_bot", "bot"
	}
	message := map[string]any{"message_id": messageID, "chat_id": "oc_chat", "chat_type": chatType, "message_type": "text", "create_time": fmt.Sprint(time.Now().UnixMilli())}
	if mentioned {
		text = "@_user_1 " + text
		message["mentions"] = []any{map[string]any{"key": "@_user_1", "id": map[string]string{"open_id": "ou_bot"}, "name": "Astral", "mentioned_type": "bot"}}
	}
	message["content"] = string(encoded(map[string]string{"text": text}))
	return encoded(map[string]any{"schema": "2.0", "header": map[string]string{"app_id": "cli_app", "event_type": "im.message.receive_v1", "event_id": eventID},
		"event": map[string]any{"sender": map[string]any{"sender_type": senderType, "sender_id": map[string]string{"open_id": senderID}}, "message": message}})
}

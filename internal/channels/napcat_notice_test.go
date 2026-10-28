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

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

func TestNapCatPokeAndRecallAreIndependentNoticeEvents(t *testing.T) {
	n := &napcat{connection: domain.Connection{Meta: domain.Meta{ID: "account"}, AccountID: "999"}}
	for _, test := range []struct {
		name, raw, kind, chat, trigger, text string
		self                                 bool
	}{
		{"group poke bot", `{"notice_type":"notify","sub_type":"poke","group_id":456,"user_id":123,"target_id":999}`, "notice.poke", "group:456", "poke", "QQ user 123 poked QQ user 999.", false},
		{"group poke member", `{"notice_type":"notify","sub_type":"poke","group_id":456,"user_id":123,"target_id":555}`, "notice.poke", "group:456", "", "QQ user 123 poked QQ user 555.", false},
		{"self poke", `{"notice_type":"notify","sub_type":"poke","group_id":456,"user_id":999,"target_id":123}`, "notice.poke", "group:456", "", "QQ user 999 poked QQ user 123.", true},
		{"private poke bot", `{"notice_type":"notify","sub_type":"poke","user_id":123,"target_id":999}`, "notice.poke", "private:123", "poke", "QQ user 123 poked QQ user 999.", false},
		{"private bot pokes peer", `{"notice_type":"notify","sub_type":"poke","user_id":123,"sender_id":999,"target_id":123}`, "notice.poke", "private:123", "", "QQ user 999 poked QQ user 123.", true},
		{"private peer pokes self", `{"notice_type":"notify","sub_type":"poke","user_id":123,"sender_id":123,"target_id":123}`, "notice.poke", "private:123", "", "QQ user 123 poked QQ user 123.", false},
		{"group recall operator", `{"notice_type":"group_recall","group_id":456,"user_id":123,"operator_id":555,"message_id":42}`, "message.deleted", "group:456", "", "QQ user 555 recalled message 42 originally sent by QQ user 123.", false},
		{"friend recall", `{"notice_type":"friend_recall","user_id":123,"message_id":42}`, "message.deleted", "private:123", "", "QQ user 123 recalled message 42 originally sent by QQ user 123.", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(`{"post_type":"notice","self_id":999,"time":1792900000,` + strings.TrimPrefix(test.raw, "{"))
			event, err := n.normalize(raw)
			if err != nil || event == nil {
				t.Fatalf("normalize notice: %v", err)
			}
			observation := event.Observation
			if observation.Kind != test.kind || event.Conversation.ExternalID != test.chat || observation.Trigger != test.trigger || observation.Self != test.self || observation.Text != test.text {
				t.Fatal("notice lost its kind, participants, chat or trigger")
			}
			if observation.Kind == "notice.poke" && observation.MessageID != "" || observation.Kind == "message.deleted" && observation.MessageID != "42" {
				t.Fatal("notice fabricated a QQ message or lost the recalled message reference")
			}
			another, err := n.normalize(raw)
			if err != nil || another.Observation.ID == observation.ID {
				t.Fatal("two received notices with identical second-level facts were collapsed")
			}
		})
	}
	raw := json.RawMessage(`{"post_type":"notice","notice_type":"notify","sub_type":"poke","event_id":"native-event","self_id":999,"time":1792900000,"group_id":456,"user_id":123,"target_id":999}`)
	first, err := n.normalize(raw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := n.normalize(raw)
	if err != nil || first.Observation.ID != second.Observation.ID {
		t.Fatal("an explicit upstream event identity did not deduplicate")
	}
}

func TestNapCatPokePreservesOfficialRawInfoCustomWording(t *testing.T) {
	// NapCat api/group.ts parsePaiYiPai and api/friend.ts parsePrivatePokeEvent
	// forward QQ json.items unchanged as raw_info (OB11PokeEvent.ts).
	n := &napcat{connection: domain.Connection{Meta: domain.Meta{ID: "account"}, AccountID: "999"}}
	for _, chat := range []string{`"group_id":456,`, `"sender_id":123,`} {
		raw := json.RawMessage(`{"post_type":"notice","notice_type":"notify","sub_type":"poke","self_id":999,"user_id":123,"target_id":999,` + chat + `"raw_info":[{"col":"1","nm":"Alice<&>","type":"qq","uid":"opaque-actor"},{"type":"img","jp":"https://example.invalid/private?token=secret","src":"https://example.invalid/action.jpg"},{"txt":"拍了拍","type":"nor"},{"col":"1","nm":"","tp":"0","type":"qq","uid":"opaque-target"},{"txt":"的 <尾巴>& 说：喝水！","type":"nor"}]}`)
		event, err := n.normalize(raw)
		if err != nil || event == nil {
			t.Fatal("official poke payload failed:", err)
		}
		want := "Alice<&> (QQ user 123) 拍了拍 QQ user 999 的 <尾巴>& 说：喝水！"
		if event.Observation.Text != want || event.Observation.Trigger != "poke" {
			t.Fatalf("custom poke wording lost: %q", event.Observation.Text)
		}
		body := messages.Observation(event.Observation, n.connection, event.Conversation, nil, "").Content[0].Text
		if !strings.Contains(body, `kind="notice.poke"`) || !strings.Contains(body, messages.Escape(want)) || strings.Contains(body, "<message ") || strings.Contains(body, "https://") || strings.Contains(body, "opaque-") || strings.Contains(body, "token=") {
			t.Fatal("poke lost its event boundary, escaping, or exposed transport metadata")
		}
	}
	for _, raw := range []string{`null`, `{}`, `[{"type":"img","src":"secret"}]`, `[{"uid":"one"},{"type":"nor","txt":"partial"}]`} {
		if text := napcatPokeText(json.RawMessage(raw), "123", "999"); text != "QQ user 123 poked QQ user 999." {
			t.Fatal("incomplete raw_info pretended to be complete platform wording")
		}
	}
}

func TestNapCatPokePendingBatchKeepsItsIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		frame := json.RawMessage(`{"post_type":"notice","notice_type":"notify","sub_type":"poke","self_id":999,"time":1792900000,"group_id":456,"user_id":123,"target_id":999}`)
		_ = wsjson.Write(r.Context(), socket, frame)
		_ = wsjson.Write(r.Context(), socket, frame)
		_, _, _ = socket.Read(r.Context())
	}))
	t.Cleanup(server.Close)
	n := napcatTestAdapter(t, server)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, err := n.Poll(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := n.Poll(ctx, "")
	if err != nil || retry.Events[0].Observation.ID != first.Events[0].Observation.ID {
		t.Fatal("retry allocated a second identity for a pending notice")
	}
	next, err := n.Poll(ctx, first.NextCursor)
	if err != nil || next.Events[0].Observation.ID == first.Events[0].Observation.ID {
		t.Fatal("a separately received poke was mistaken for pending replay")
	}
}

func TestNapCatNoticesAppendToContextAndRespectChatBinding(t *testing.T) {
	frames := make(chan json.RawMessage, 10)
	var sockets atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_login_info":
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"user_id":999,"nickname":"Astral"}}`)
		case "/get_status":
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"online":true}}`)
		case "/get_group_info":
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"group_name":"Team"}}`)
		case "/ws":
			socket, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer socket.CloseNow()
			_ = wsjson.Write(r.Context(), socket, map[string]any{"post_type": "meta_event", "self_id": 999})
			if sockets.Add(1) == 1 {
				_, _, _ = socket.Read(r.Context()) // identity validation connection
				return
			}
			done := make(chan struct{})
			go func() { _, _, _ = socket.Read(r.Context()); close(done) }()
			for {
				select {
				case frame := <-frames:
					if wsjson.Write(r.Context(), socket, frame) != nil {
						return
					}
				case <-done:
					return
				case <-r.Context().Done():
					return
				}
			}
		default:
			t.Errorf("unexpected NapCat API call: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	p := testkit.New(t, 1, func(_ context.Context, n int, request map[string]any) any {
		if n == 2 {
			body := fmt.Sprint(request["messages"])
			for _, want := range []string{"original-kept", `kind="message.deleted"`, `message-id="42"`, "QQ user 555 recalled message 42", `kind="notice.poke"`, "QQ user 123 poked QQ user 777.", `reason="poke"`} {
				if !strings.Contains(body, want) {
					t.Errorf("appended context lost %q", want)
				}
			}
		}
		return testkit.Text("received")
	})
	service := New(p.Store, p.Harness)
	connection := domain.Connection{Name: "NapCat", AgentID: p.Agent.ID, Adapter: "qq.napcat", Enabled: true, Config: encoded(NapCatConfig{BaseURL: server.URL, WebSocketURL: strings.Replace(server.URL, "http://", "ws://", 1) + "/ws"})}
	secret := "fixture"
	if err := service.SaveConnection(p.Context, &connection, &secret); err != nil {
		t.Fatal(err)
	}
	binding := createBinding(t, p, service, connection, "group:456", "")
	startChannels(t, service, p.Context)
	frames <- json.RawMessage(`{"post_type":"message","message_type":"group","self_id":999,"message_id":42,"user_id":123,"group_id":456,"time":1792900000,"message":[{"type":"at","data":{"qq":999}},{"type":"text","data":{"text":"original-kept"}}]}`)
	testkit.Await(t, p.Context, func() bool { return p.Requests.Load() == 1 })
	p.Idle(t, binding.SessionID)
	originalID := stableID("event", connection.ID, "message.created:group:456:42")
	originalDelivery, err := service.Delivery(p.Context, originalID)
	if err != nil {
		t.Fatal(err)
	}
	frames <- json.RawMessage(`{"post_type":"notice","notice_type":"group_recall","self_id":999,"message_id":42,"user_id":123,"operator_id":555,"group_id":456,"time":1792900001}`)
	frames <- json.RawMessage(`{"post_type":"notice","notice_type":"notify","sub_type":"poke","self_id":999,"user_id":123,"target_id":777,"group_id":456,"time":1792900002}`)
	testkit.Await(t, p.Context, func() bool {
		events, _ := p.Store.Observations(p.Context, binding.ConversationID, 0, 100)
		if len(events) != 3 {
			return false
		}
		delivery, e := service.Delivery(p.Context, events[2].Observation.ID)
		return e == nil && delivery.InputID != ""
	})
	if p.Requests.Load() != 1 {
		t.Fatal("recall or a poke of another member activated the model")
	}
	frames <- json.RawMessage(`{"post_type":"notice","notice_type":"notify","sub_type":"poke","self_id":999,"user_id":123,"target_id":999,"group_id":456,"time":1792900003}`)
	testkit.Await(t, p.Context, func() bool { return p.Requests.Load() == 2 })
	p.Idle(t, binding.SessionID)
	original, err := p.Store.Observation(p.Context, originalID)
	if err != nil || original.Kind != "message.created" || !strings.Contains(original.Text, "original-kept") {
		t.Fatal("recall overwrote the original archived observation")
	}
	after, err := service.Delivery(p.Context, originalID)
	if err != nil || after.EntryID != originalDelivery.EntryID || after.InputID != originalDelivery.InputID {
		t.Fatal("recall changed the original Core receipt")
	}
	frames <- json.RawMessage(`{"post_type":"notice","notice_type":"notify","sub_type":"poke","self_id":999,"user_id":123,"target_id":999,"group_id":789,"time":1792900004}`)
	unboundID := stableID("conversation", connection.ID, "group:789", "")
	testkit.Await(t, p.Context, func() bool {
		events, _ := p.Store.Observations(p.Context, unboundID, 0, 100)
		var position cursor
		return len(events) == 1 && p.Store.Get(p.Context, "channel_cursors", connection.ID, &position) == nil && position.Value == events[0].Observation.ID
	})
	bindings, err := list[domain.ConversationBinding](p.Context, p.Store, "bindings")
	if err != nil || len(bindings) != 1 || p.Requests.Load() != 2 {
		t.Fatal("a poke bypassed specified-chat access")
	}
}

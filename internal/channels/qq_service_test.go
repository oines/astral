package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestQQServiceMaintainsSessionAndExplicitSendReceipts(t *testing.T) {
	p := testkit.New(t, 1, func(_ context.Context, n int, request map[string]any) any {
		messages := string(encoded(request["messages"]))
		if n == 1 && strings.Contains(messages, "before activation") {
			t.Error("new QQ Session replayed pre-activation history")
		}
		if n == 3 && (!strings.Contains(messages, "first QQ request") || !strings.Contains(messages, "passive context between requests")) {
			t.Error("QQ continuation lost the bound Session's sustained context")
		}
		if n%2 == 1 {
			return testkit.Tool("send_message", map[string]any{"text": fmt.Sprintf("explicit reply %d", (n+1)/2)})
		}
		return testkit.Text("This completion remains in Astral.")
	})
	type dispatch struct {
		kind string
		data map[string]any
	}
	events := make(chan dispatch, 20)
	var sends atomic.Int64
	var mu sync.Mutex
	var replies, texts []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			fmt.Fprint(w, `{"access_token":"access","expires_in":7200}`)
		case "/users/@me":
			fmt.Fprint(w, `{"id":"bot-1","username":"Astral","bot":true}`)
		case "/gateway":
			_ = json.NewEncoder(w).Encode(map[string]string{"url": strings.Replace(server.URL, "http://", "ws://", 1) + "/ws"})
		case "/ws":
			socket, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer socket.CloseNow()
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			_ = wsjson.Write(ctx, socket, map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 1000}})
			var identify json.RawMessage
			if err := wsjson.Read(ctx, socket, &identify); err != nil {
				return
			}
			_ = wsjson.Write(ctx, socket, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]string{"session_id": "service-qq-session"}})
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				for {
					var frame struct {
						Op int `json:"op"`
					}
					if wsjson.Read(ctx, socket, &frame) != nil {
						return
					}
					if frame.Op == 1 {
						_ = wsjson.Write(ctx, socket, map[string]any{"op": 11})
					}
				}
			}()
			sequence := 1
			for {
				select {
				case event := <-events:
					sequence++
					if wsjson.Write(ctx, socket, map[string]any{"op": 0, "t": event.kind, "s": sequence, "d": event.data}) != nil {
						return
					}
				case <-ctx.Done():
					return
				case <-readDone:
					return
				}
			}
		case "/v2/groups/g-1/messages":
			var body struct {
				Content string `json:"content"`
				Reply   string `json:"msg_id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid outbound request")
			}
			mu.Lock()
			replies, texts = append(replies, body.Reply), append(texts, body.Content)
			mu.Unlock()
			n := sends.Add(1)
			messageID := fmt.Sprintf("out-%d", n)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": messageID})
			// The platform's self echo must converge with the receipt archive.
			events <- dispatch{kind: "GROUP_MESSAGE_CREATE", data: map[string]any{"id": messageID, "group_openid": "g-1", "content": body.Content,
				"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "author": map[string]any{"member_openid": "bot-1", "bot": true}}}
		default:
			t.Errorf("unexpected QQ endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	service := New(p.Store, p.Harness)
	connection := domain.Connection{Name: "QQ integration", AgentID: p.Agent.ID, Adapter: "qq.bot", Enabled: true, AccessMode: "all",
		Config: encoded(QQBotConfig{AppID: "app-123", BaseURL: server.URL, TokenURL: server.URL + "/token"})}
	secret := "app-secret"
	if err := service.SaveConnection(p.Context, &connection, &secret); err != nil {
		t.Fatal(err)
	}
	chat, err := service.ResolveForAgent(p.Context, p.Agent.ID, connection.ID, "group:g-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if bindings, _ := list[domain.ConversationBinding](p.Context, p.Store, "bindings"); len(bindings) != 0 {
		t.Fatal("QQ API resolution created a Session")
	}
	startChannels(t, service, p.Context)
	push := func(kind, id, text string) {
		events <- dispatch{kind: kind, data: map[string]any{"id": id, "group_openid": "g-1", "content": text,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "author": map[string]string{"member_openid": "member-1"}}}
	}
	push("GROUP_MESSAGE_CREATE", "before", "before activation")
	testkit.Await(t, p.Context, func() bool { _, err := p.Store.Message(p.Context, chat.ID, "before"); return err == nil })
	if bindings, _ := list[domain.ConversationBinding](p.Context, p.Store, "bindings"); len(bindings) != 0 {
		t.Fatal("ordinary QQ group message activated an all-mode Session")
	}
	push("GROUP_AT_MESSAGE_CREATE", "m-1", "first QQ request")
	var binding domain.ConversationBinding
	testkit.Await(t, p.Context, func() bool {
		binding, _ = service.activeBinding(p.Context, chat.ID)
		return binding.ID != ""
	})
	waitReceipts := func(count int) {
		t.Helper()
		testkit.Await(t, p.Context, func() bool {
			outbox, _ := list[domain.Outbox](p.Context, p.Store, "outbox")
			if len(outbox) != count {
				return false
			}
			for _, record := range outbox {
				if record.Status != "accepted" || len(record.PlatformMessageIDs) != 1 {
					return false
				}
			}
			return true
		})
	}
	waitReceipts(1)
	p.Idle(t, binding.SessionID)
	push("GROUP_MESSAGE_CREATE", "between", "passive context between requests")
	testkit.Await(t, p.Context, func() bool { _, err := p.Store.Message(p.Context, chat.ID, "between"); return err == nil })
	push("GROUP_AT_MESSAGE_CREATE", "m-2", "second QQ request")
	waitReceipts(2)
	p.Idle(t, binding.SessionID)
	current, _ := service.activeBinding(p.Context, chat.ID)
	if current.SessionID != binding.SessionID || p.Requests.Load() != 4 || sends.Load() != 2 {
		t.Fatalf("QQ conversation did not reuse its Session: original=%+v current=%+v requests=%d sends=%d", binding, current, p.Requests.Load(), sends.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(replies, ",") != "m-1,m-2" || strings.Join(texts, ",") != "explicit reply 1,explicit reply 2" {
		t.Fatalf("fresh send adapters lost persistent msg_id or emitted uncommitted text: replies=%v texts=%v", replies, texts)
	}
}

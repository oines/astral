package channels

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func qqTestAdapter(t *testing.T, server *httptest.Server) *qqbot {
	t.Helper()
	a, err := NewQQBot(domain.Connection{Meta: domain.Meta{ID: "qq-account"}, AccountID: "bot-1", Config: encoded(QQBotConfig{AppID: "123", BaseURL: server.URL, TokenURL: server.URL + "/token"})}, "app-secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return a.(*qqbot)
}

func TestQQGatewayIdentifyHeartbeatReplayAndResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var connections atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			var params map[string]string
			_ = json.NewDecoder(r.Body).Decode(&params)
			if params["clientSecret"] != "app-secret" || params["appId"] != "123" {
				t.Error("invalid token credentials")
			}
			fmt.Fprint(w, `{"access_token":"access","expires_in":"7200"}`)
		case "/users/@me":
			if r.Header.Get("Authorization") != "QQBot access" {
				t.Error("invalid API authorization")
			}
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
			_ = wsjson.Write(ctx, socket, map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 100}})
			var frame struct {
				Op   int            `json:"op"`
				Data map[string]any `json:"d"`
			}
			if err := wsjson.Read(ctx, socket, &frame); err != nil {
				t.Error(err)
				return
			}
			if frame.Data["token"] != "QQBot access" {
				t.Error("gateway token missing")
			}
			if connections.Add(1) == 1 {
				if frame.Op != 2 || frame.Data["intents"] != float64(1<<25) {
					t.Errorf("invalid identify: %+v", frame)
				}
				_ = wsjson.Write(ctx, socket, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]string{"session_id": "qq-session"}})
				var heartbeat struct {
					Op   int   `json:"op"`
					Data int64 `json:"d"`
				}
				if err := wsjson.Read(ctx, socket, &heartbeat); err != nil {
					t.Error(err)
					return
				}
				if heartbeat.Op != 1 || heartbeat.Data != 1 {
					t.Errorf("invalid heartbeat: %+v", heartbeat)
				}
				_ = wsjson.Write(ctx, socket, map[string]any{"op": 11})
				_ = wsjson.Write(ctx, socket, map[string]any{"op": 0, "t": "GROUP_AT_MESSAGE_CREATE", "s": 2, "d": map[string]any{"id": "m-1", "group_openid": "g-1", "content": "hello", "timestamp": "2026-10-23T10:00:00Z", "author": map[string]string{"member_openid": "u-1"}}})
				_ = socket.Close(websocket.StatusGoingAway, "reconnect")
			} else {
				if frame.Op != 6 || frame.Data["session_id"] != "qq-session" || frame.Data["seq"] != float64(2) {
					t.Errorf("invalid resume: %+v", frame)
				}
				_ = wsjson.Write(ctx, socket, map[string]any{"op": 0, "t": "RESUMED", "s": 3, "d": map[string]any{}})
				_, _, _ = socket.Read(ctx)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	q := qqTestAdapter(t, server)
	defer q.dropSocket()
	identity, err := q.Validate(ctx)
	if err != nil || identity.ID != "bot-1" || len(identity.Diagnostics) == 0 {
		t.Fatalf("identity: %+v %v", identity, err)
	}
	ready, err := q.Poll(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := q.Poll(ctx, ready.NextCursor)
	if err != nil || len(batch.Events) != 1 {
		t.Fatalf("message: %+v %v", batch, err)
	}
	event := batch.Events[0]
	if event.Conversation.ExternalID != "group:g-1" || event.Observation.Trigger != "mention" || event.Observation.Text != "hello" {
		t.Fatalf("normalize: %+v", event)
	}
	replay, err := q.Poll(ctx, ready.NextCursor)
	if err != nil || replay.Events[0].Observation.ID != event.Observation.ID {
		t.Fatal("uncommitted batch was lost")
	}
	_, err = q.Poll(ctx, batch.NextCursor)
	if err == nil {
		t.Fatal("expected socket close")
	}
	resumed, err := q.Poll(ctx, batch.NextCursor)
	if err != nil || !strings.Contains(resumed.NextCursor, `"sequence":3`) {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
}

func TestQQSendUsesPersistedReplyAndUploadsBeforeSingleSend(t *testing.T) {
	ctx := context.Background()
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"access","expires_in":7200}`)
			return
		}
		if r.Header.Get("Authorization") != "QQBot access" {
			t.Error("authorization missing")
		}
		var params map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&params)
		switch r.URL.Path {
		case "/v2/groups/g-1/files":
			var data string
			_ = json.Unmarshal(params["file_data"], &data)
			decoded, _ := base64.StdEncoding.DecodeString(data)
			if string(decoded) != "file content" || string(params["srv_send_msg"]) != "false" {
				t.Error("upload auto-sent or content corrupted")
			}
			fmt.Fprint(w, `{"file_info":"media-handle"}`)
		case "/v2/groups/g-1/messages":
			sends.Add(1)
			if string(params["msg_id"]) != `"incoming-1"` || string(params["msg_type"]) != "7" || params["msg_seq"] == nil {
				t.Errorf("invalid send: %+v", params)
			}
			fmt.Fprint(w, `{"id":"outgoing-1"}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	conversationID := stableID("conversation", "qq-account", "group:g-1", "")
	_, err = store.Archive(ctx, domain.Observation{ID: "event-1", ConnectionID: "qq-account", ConversationID: conversationID, ExternalEventID: "incoming-1", MessageID: "incoming-1", Kind: "message.created", SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	// The send adapter is a fresh factory instance, just like Service.dispatch.
	q := qqTestAdapter(t, server)
	q.setStore(store)
	receipt, err := q.Send(ctx, Send{Target: domain.Conversation{ExternalID: "group:g-1"}, Text: "caption", File: io.NopCloser(strings.NewReader("file content")), FileName: "report.txt", MediaType: "text/plain"})
	if err != nil || len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "outgoing-1" || sends.Load() != 1 {
		t.Fatalf("send: %+v %v", receipt, err)
	}
}

func TestQQSendUncertainReceiptAndDefinitiveFailure(t *testing.T) {
	for _, test := range []struct {
		name, response string
		status         int
		unknown        bool
	}{
		{"rate limited", `{"code":429}`, 429, false},
		{"invalid response", `<html>gateway failed</html>`, 200, true},
		{"missing receipt", `{}`, 200, true},
		{"server failure", `{"code":500}`, 500, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					fmt.Fprint(w, `{"access_token":"access","expires_in":"7200"}`)
					return
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.response)
			}))
			defer server.Close()
			_, err := qqTestAdapter(t, server).Send(context.Background(), Send{Target: domain.Conversation{ExternalID: "private:u-1"}, Text: "hello"})
			var sendErr *SendError
			if !errors.As(err, &sendErr) || sendErr.Unknown != test.unknown {
				t.Fatalf("wrong receipt semantics: %+v", err)
			}
		})
	}
}

func TestQQNormalizesOrdinaryGroupEventsAndSafeDownloads(t *testing.T) {
	q := &qqbot{connection: domain.Connection{Meta: domain.Meta{ID: "qq"}, AccountID: "self"}}
	raw := json.RawMessage(`{"id":"m","group_openid":"g","content":"hi","author":{"member_openid":"member"},"attachments":[{"url":"https://media.example/file","filename":"x.pdf","content_type":"application/pdf","size":4}]}`)
	ordinary, err := q.normalize("GROUP_MESSAGE_CREATE", raw)
	if err != nil || ordinary.Observation.Trigger != "" || len(ordinary.Observation.Attachments) != 1 {
		t.Fatalf("ordinary event: %+v %v", ordinary, err)
	}
	mention, err := q.normalize("GROUP_AT_MESSAGE_CREATE", raw)
	if err != nil || mention.Observation.ID != ordinary.Observation.ID {
		t.Fatal("same QQ message did not deduplicate across event forms")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credential leaked to media host")
		}
		fmt.Fprint(w, "data")
	}))
	defer server.Close()
	q.client = qqHTTPClient(server.Client())
	file, err := q.Download(context.Background(), domain.Attachment{ExternalID: server.URL + "/file?signature=x", Name: "f"})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Body.Close()
	data, _ := io.ReadAll(file.Body)
	if !bytes.Equal(data, []byte("data")) {
		t.Fatal("download content mismatch")
	}
	for _, id := range []string{"123", "group:../escape", "private:", "channel:1"} {
		if _, _, err := qqTarget(id); err == nil {
			t.Fatalf("accepted invalid address %q", id)
		}
	}
}

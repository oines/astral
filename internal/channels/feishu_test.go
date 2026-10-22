package channels

import (
	"bytes"
	"context"
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
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/oines/astral/internal/domain"
)

type feishuFixture struct {
	server       *httptest.Server
	adapter      *feishu
	events       chan []byte
	acks         chan larkws.Frame
	sends        chan map[string]any
	uploads      chan []byte
	authCount    atomic.Int64
	apiCode      atomic.Int64
	interrupted  atomic.Bool
	missingID    atomic.Bool
	deleted      atomic.Int64
	reactions    atomic.Int64
	chatRequests atomic.Int64
}

func newFeishuFixture(t *testing.T) *feishuFixture {
	t.Helper()
	f := &feishuFixture{events: make(chan []byte, 16), acks: make(chan larkws.Frame, 16), sends: make(chan map[string]any, 16), uploads: make(chan []byte, 16)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "success", "data": data})
		}
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			var credentials map[string]string
			_ = json.NewDecoder(r.Body).Decode(&credentials)
			if credentials["app_id"] != "cli_app" || credentials["app_secret"] != "app-secret" {
				t.Errorf("unexpected credentials: %#v", credentials)
			}
			f.authCount.Add(1)
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"tenant-token","expire":7200}`)
			return
		case "/callback/ws/endpoint":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"URL": "ws" + strings.TrimPrefix(f.server.URL, "http") + "/ws?service_id=1&device_id=test", "ClientConfig": map[string]int{"PingInterval": 30, "ReconnectCount": 0, "ReconnectInterval": 1}}})
			return
		case "/ws":
			f.serveWebsocket(t, w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tenant-token" {
			t.Errorf("missing authorization on %s", r.URL.Path)
		}
		switch {
		case r.URL.Path == "/open-apis/bot/v3/info":
			_, _ = io.WriteString(w, `{"code":0,"bot":{"open_id":"ou_bot","app_name":"Astral","activate_status":2}}`)
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/chats/"):
			f.chatRequests.Add(1)
			reply(map[string]any{"name": "测试群", "chat_mode": "group"})
		case r.URL.Path == "/open-apis/im/v1/files":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			if r.FormValue("file_type") != "stream" || r.FormValue("file_name") != "report.json" {
				t.Errorf("wrong upload fields: %#v", r.Form)
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			raw, _ := io.ReadAll(file)
			f.uploads <- raw
			reply(map[string]string{"file_key": "file_v2_abc-def"})
		case strings.Contains(r.URL.Path, "/resources/"):
			if r.URL.Query().Get("type") != "file" {
				t.Errorf("wrong resource type: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", `attachment; filename="report.json"`)
			_, _ = io.WriteString(w, `{"downloaded":true}`)
		case strings.HasSuffix(r.URL.Path, "/reactions"):
			f.reactions.Add(1)
			reply(map[string]string{"reaction_id": "reaction_1"})
		case r.URL.Path == "/open-apis/im/v1/messages" && r.Method == http.MethodGet:
			if r.URL.Query().Get("container_id") != "oc_chat" || r.URL.Query().Get("container_id_type") != "chat" {
				t.Errorf("history escaped selected chat: %s", r.URL.RawQuery)
			}
			reply(map[string]any{"items": []any{}, "has_more": false})
		case r.URL.Path == "/open-apis/im/v1/messages" || strings.HasSuffix(r.URL.Path, "/reply"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.sends <- body
			if f.interrupted.Load() {
				connection, _, _ := w.(http.Hijacker).Hijack()
				_ = connection.Close()
				return
			}
			if code := f.apiCode.Load(); code != 0 {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "failure app-secret tenant-token"})
				return
			}
			if f.missingID.Load() {
				reply(map[string]any{})
				return
			}
			reply(map[string]string{"message_id": "om_sent"})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			if r.Method == http.MethodDelete {
				f.deleted.Add(1)
				reply(map[string]any{})
				return
			}
			id := strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
			chat := "oc_chat"
			if id == "om_other" {
				chat = "oc_other"
			}
			reply(map[string]any{"items": []any{map[string]any{"message_id": id, "chat_id": chat, "msg_type": "text", "sender": map[string]string{"id": "cli_app", "sender_type": "app"}, "body": map[string]string{"content": `{"text":"Prior Bot answer"}`}}}})
		default:
			t.Errorf("unexpected Feishu request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	adapter, err := NewFeishu(domain.Connection{Meta: domain.Meta{ID: "connection_feishu"}, AccountID: "ou_bot", Config: encoded(FeishuConfig{AppID: "cli_app", BaseURL: f.server.URL})}, "app-secret", f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	f.adapter = adapter.(*feishu)
	t.Cleanup(f.server.Close)
	return f
}

func (f *feishuFixture) serveWebsocket(t *testing.T, w http.ResponseWriter, r *http.Request) {
	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Error(err)
		return
	}
	defer socket.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer cancel()
		for {
			_, raw, err := socket.Read(ctx)
			if err != nil {
				return
			}
			var frame larkws.Frame
			if frame.Unmarshal(raw) != nil {
				t.Error("invalid client protobuf frame")
				return
			}
			if frame.Method == int32(larkws.FrameTypeData) {
				f.acks <- frame
			}
		}
	}()
	for {
		select {
		case payload := <-f.events:
			frame := larkws.Frame{Method: int32(larkws.FrameTypeData), Service: 1, Headers: []larkws.Header{{Key: "type", Value: "event"}, {Key: "message_id", Value: "ws-message"}, {Key: "sum", Value: "1"}}, Payload: payload}
			raw, _ := frame.Marshal()
			if socket.Write(ctx, websocket.MessageBinary, raw) != nil {
				return
			}
		case <-readDone:
			return
		case <-ctx.Done():
			return
		}
	}
}

func feishuTestEvent(id, chatType, messageType, content string) []byte {
	return encoded(map[string]any{"schema": "2.0", "header": map[string]string{"event_id": id, "app_id": "cli_app", "event_type": "im.message.receive_v1"}, "event": map[string]any{
		"sender":  map[string]any{"sender_id": map[string]string{"open_id": "ou_user"}, "sender_type": "user"},
		"message": map[string]any{"message_id": "om_incoming", "chat_id": "oc_chat", "chat_type": chatType, "message_type": messageType, "content": content, "create_time": "1700000000000"},
	}})
}

func TestFeishuValidateAndTextFileRoundTrip(t *testing.T) {
	f := newFeishuFixture(t)
	ctx := context.Background()
	identity, err := f.adapter.Validate(ctx)
	if err != nil || identity.ID != "ou_bot" || identity.Name != "Astral" || len(identity.Diagnostics) == 0 {
		t.Fatalf("identity: %#v, %v", identity, err)
	}
	target := domain.Conversation{ConnectionID: "connection_feishu", ExternalID: "oc_chat"}
	receipt, err := f.adapter.Send(ctx, Send{Target: target, Text: "你好 🌍"})
	if err != nil || len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "om_sent" {
		t.Fatalf("text send: %#v, %v", receipt, err)
	}
	body := <-f.sends
	if body["receive_id"] != "oc_chat" || body["msg_type"] != "text" {
		t.Fatalf("text destination: %#v", body)
	}
	var content map[string]string
	_ = json.Unmarshal([]byte(body["content"].(string)), &content)
	if content["text"] != "你好 🌍" {
		t.Fatalf("text encoding: %#v", content)
	}
	file := []byte(`{"hello":"world"}`)
	receipt, err = f.adapter.Send(ctx, Send{Target: target, File: io.NopCloser(bytes.NewReader(file)), FileName: "report.json", Size: int64(len(file))})
	if err != nil || len(receipt.MessageIDs) != 1 {
		t.Fatalf("file send: %#v, %v", receipt, err)
	}
	if !bytes.Equal(<-f.uploads, file) {
		t.Fatal("uploaded bytes changed")
	}
	body = <-f.sends
	if body["msg_type"] != "file" || !strings.Contains(body["content"].(string), "file_v2_abc-def") {
		t.Fatalf("file message: %#v", body)
	}
	download, err := f.adapter.Download(ctx, domain.Attachment{ExternalID: "om_incoming/file_v2_abc-def", Kind: "file"})
	if err != nil {
		t.Fatal(err)
	}
	defer download.Body.Close()
	raw, err := io.ReadAll(download.Body)
	if err != nil || string(raw) != `{"downloaded":true}` || download.Name != "report.json" {
		t.Fatalf("download: %q, %#v, %v", raw, download, err)
	}
	if f.authCount.Load() != 1 {
		t.Fatalf("token was not reused: %d auth requests", f.authCount.Load())
	}
}

func TestFeishuNormalizeMentionsRichTextAndPrivateReplies(t *testing.T) {
	f := newFeishuFixture(t)
	var message larkim.P2MessageReceiveV1
	raw := feishuTestEvent("event_1", "group", "text", `{"text":"@_user_1 帮我看看"}`)
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	name, key, bot := "Astral", "@_user_1", "ou_bot"
	message.Event.Message.Mentions = []*larkim.MentionEvent{{Key: &key, Name: &name, Id: &larkim.UserId{OpenId: &bot}}}
	event, err := f.adapter.normalize(&message)
	if err != nil || event.Observation.Trigger != "mention" || event.Observation.Text != "@Astral 帮我看看" || event.Conversation.Kind != "group" {
		t.Fatalf("mention: %#v, %v", event, err)
	}
	var duplicate larkim.P2MessageReceiveV1
	_ = json.Unmarshal(raw, &duplicate)
	again, _ := f.adapter.normalize(&duplicate)
	if event.Observation.ID != again.Observation.ID {
		t.Fatal("redelivered event got a different archive identity")
	}
	parent := "om_parent"
	message.Event.Message.ParentId = &parent
	message.Event.Message.Mentions = nil
	event, _ = f.adapter.normalize(&message)
	f.adapter.enrichReply(context.Background(), &event)
	if event.Observation.Trigger != "reply" || event.Observation.ReplyPreview != "Prior Bot answer" {
		t.Fatalf("reply: %#v", event.Observation)
	}
	_ = json.Unmarshal(feishuTestEvent("event_2", "p2p", "text", `{"text":"hello"}`), &message)
	event, err = f.adapter.normalize(&message)
	if err != nil || event.Observation.Trigger != "private" || event.Conversation.Kind != "private" {
		t.Fatalf("private: %#v, %v", event, err)
	}
	text, attachments := feishuContent("post", `{"title":"附件","content":[[{"tag":"text","text":"hello"},{"tag":"a","text":"docs","href":"https://open.feishu.cn"}],[{"tag":"img","image_key":"img_v3_abc-def"}]]}`, "om_post")
	if !strings.Contains(text, "附件\nhellodocs") || len(attachments) != 1 || attachments[0].ExternalID != "om_post/img_v3_abc-def" {
		t.Fatalf("post: %q, %#v", text, attachments)
	}
	message.EventV2Base.Header.AppID = "cli_other"
	if _, err = f.adapter.normalize(&message); err == nil {
		t.Fatal("accepted another application's event")
	}
}

func TestFeishuWebsocketWaitsForDurableCursorBeforeACK(t *testing.T) {
	f := newFeishuFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f.events <- feishuTestEvent("event_ack", "p2p", "text", `{"text":"hello"}`)
	batch, err := f.adapter.Poll(ctx, "")
	if err != nil || batch.NextCursor != "event_ack" || len(batch.Events) != 1 || batch.Events[0].Observation.Text != "hello" {
		t.Fatalf("poll: %#v, %v", batch, err)
	}
	select {
	case ack := <-f.acks:
		t.Fatalf("event was acknowledged before persistence: %#v", ack)
	case <-time.After(40 * time.Millisecond):
	}
	// A failed persistence attempt must replay the identical event, retaining
	// its callback until the service can advance the committed cursor.
	replayed, err := f.adapter.Poll(ctx, "")
	if err != nil || replayed.Events[0].Observation.ID != batch.Events[0].Observation.ID {
		t.Fatalf("failed to replay uncommitted event: %#v, %v", replayed, err)
	}
	f.events <- feishuTestEvent("event_next", "p2p", "text", `{"text":"next"}`)
	next, err := f.adapter.Poll(ctx, batch.NextCursor)
	if err != nil || next.NextCursor != "event_next" {
		t.Fatalf("next poll: %#v, %v", next, err)
	}
	select {
	case ack := <-f.acks:
		var response struct {
			Code int `json:"code"`
		}
		if json.Unmarshal(ack.Payload, &response) != nil || response.Code != http.StatusOK {
			t.Fatalf("bad ACK: %s", ack.Payload)
		}
	case <-ctx.Done():
		t.Fatal("no ACK after durable cursor advanced")
	}
	if f.chatRequests.Load() != 1 {
		t.Fatalf("chat metadata was not cached: %d", f.chatRequests.Load())
	}
}

func TestFeishuSendReceiptsAndScopeFailures(t *testing.T) {
	for _, scenario := range []string{"known", "unknown", "missing_receipt"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFeishuFixture(t)
			switch scenario {
			case "known":
				f.apiCode.Store(230020)
			case "unknown":
				f.interrupted.Store(true)
			case "missing_receipt":
				f.missingID.Store(true)
			}
			_, err := f.adapter.Send(context.Background(), Send{Target: domain.Conversation{ExternalID: "oc_chat"}, Text: "hello"})
			var sendError *SendError
			if !errors.As(err, &sendError) || sendError.Unknown != (scenario != "known") {
				t.Fatalf("wrong send result: %#v, %v", sendError, err)
			}
			if strings.Contains(err.Error(), "app-secret") || strings.Contains(err.Error(), "tenant-token") {
				t.Fatalf("credentials leaked: %s", err)
			}
		})
	}
	f := newFeishuFixture(t)
	_, err := f.adapter.Send(context.Background(), Send{Target: domain.Conversation{ExternalID: "oc_chat"}, ReplyTo: "om_other", Text: "secret"})
	if err == nil || len(f.sends) != 0 {
		t.Fatalf("cross-chat reply reached platform: %v", err)
	}
	_, err = f.adapter.Send(context.Background(), Send{Target: domain.Conversation{ExternalID: "oc_chat"}, Text: "caption", File: io.NopCloser(strings.NewReader("x")), FileName: "report.json", Size: 1})
	if err == nil || len(f.uploads) != 0 {
		t.Fatalf("unsupported caption was uploaded: %v", err)
	}
	if _, err = f.adapter.ResolveConversation(context.Background(), "oc_chat", "thread_id"); err == nil {
		t.Fatal("accepted an unsupported Topic grant")
	}
}

func TestFeishuOperationsStayInSelectedChat(t *testing.T) {
	f := newFeishuFixture(t)
	target := domain.Conversation{ConnectionID: "connection_feishu", ExternalID: "oc_chat"}
	ctx := context.Background()
	for name, parameters := range map[string]string{
		"get_chat": `{}`, "get_message": `{"message_id":"om_parent"}`, "list_messages": `{"page_size":10,"sort_type":"ByCreateTimeAsc"}`,
		"add_reaction": `{"message_id":"om_parent","emoji_type":"OK"}`, "delete_message": `{"message_id":"om_parent"}`,
	} {
		if _, err := f.adapter.operation(ctx, name, target, json.RawMessage(parameters)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if f.deleted.Load() != 1 || f.reactions.Load() != 1 {
		t.Fatalf("operations did not reach platform: deleted=%d reactions=%d", f.deleted.Load(), f.reactions.Load())
	}
	for name, parameters := range map[string]string{
		"get_chat": `{"chat_id":"oc_other"}`, "get_message": `{"message_id":"om_other"}`, "delete_message": `{"message_id":"om_other"}`,
		"add_reaction": `{"message_id":"om_other","emoji_type":"OK"}`, "list_messages": `{"page_size":100000}`, "unknown": `{}`,
	} {
		if _, err := f.adapter.operation(ctx, name, target, json.RawMessage(parameters)); err == nil {
			t.Fatalf("accepted invalid %s: %s", name, parameters)
		}
	}
	if f.deleted.Load() != 1 || f.reactions.Load() != 1 {
		t.Fatal("a rejected cross-chat mutation reached the platform")
	}
}

func TestFeishuConfigurationAndTextBounds(t *testing.T) {
	for _, configuration := range []string{`{}`, `{"app_id":"cli_app","unknown":true}`, `{"app_id":"../app"}`, `{"app_id":"cli_app","base_url":"https://example.com?token=secret"}`} {
		if _, err := NewFeishu(domain.Connection{Config: json.RawMessage(configuration)}, "secret", nil); err == nil {
			t.Fatalf("accepted invalid config: %s", configuration)
		}
	}
	f := newFeishuFixture(t)
	text := strings.Repeat("你好🌍\x01", 10000)
	parts, err := f.adapter.SplitText(text)
	if err != nil || strings.Join(parts, "") != text || len(parts) < 2 {
		t.Fatalf("split: %d parts, %v", len(parts), err)
	}
	for index, part := range parts {
		body := encoded(map[string]any{"receive_id": "oc_chat", "msg_type": "text", "content": string(encoded(map[string]string{"text": part}))})
		if len(body) > 150_000 {
			t.Fatalf("fragment %d exceeds Feishu JSON limit: %d", index, len(body))
		}
	}
	for _, value := range []string{"", "../key", "key?a=b", "key/x", "key\n"} {
		if feishuID(value) {
			t.Fatalf("unsafe platform ID accepted: %q", value)
		}
	}
	if !feishuID("file_v2_abc-def") {
		t.Fatal(fmt.Errorf("valid Feishu file key rejected"))
	}
}

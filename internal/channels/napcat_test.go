package channels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
)

func napcatTestAdapter(t *testing.T, server *httptest.Server) *napcat {
	t.Helper()
	a, err := NewNapCat(domain.Connection{Meta: domain.Meta{ID: "napcat-account"}, AccountID: "999", Config: encoded(NapCatConfig{BaseURL: server.URL, WebSocketURL: strings.Replace(server.URL, "http://", "ws://", 1) + "/ws"})}, "access", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return a.(*napcat)
}

func TestNapCatForwardWebSocketAndPendingReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Error("NapCat authorization missing")
		}
		switch r.URL.Path {
		case "/get_login_info":
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"user_id":999,"nickname":"Astral"}}`)
		case "/get_status":
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"online":true}}`)
		case "/ws":
			socket, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer socket.CloseNow()
			_ = wsjson.Write(ctx, socket, map[string]any{"post_type": "meta_event", "meta_event_type": "lifecycle", "self_id": 999})
			_ = wsjson.Write(ctx, socket, json.RawMessage(`{"post_type":"message","message_type":"group","self_id":999,"message_id":-123,"user_id":123,"group_id":456,"group_name":"Team","time":1780000000,"sender":{"nickname":"Alice","card":"A"},"message":[{"type":"at","data":{"qq":"999"}},{"type":"text","data":{"text":" hello"}},{"type":"reply","data":{"id":"-42"}},{"type":"image","data":{"file":"opaque-file","url":"https://media.example/image","file_size":"42","summary":"[image]"}},{"type":"forward","data":{"content":[{"nested":true}]}}]}`))
			_ = wsjson.Write(ctx, socket, json.RawMessage(`{"post_type":"notice","notice_type":"group_recall","self_id":999,"message_id":-123,"group_id":456,"user_id":123,"time":1780000001}`))
			_, _, _ = socket.Read(ctx)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	n := napcatTestAdapter(t, server)
	identity, err := n.Validate(ctx)
	if err != nil || identity.ID != "999" {
		t.Fatalf("validate: %+v %v", identity, err)
	}
	batch, err := n.Poll(ctx, "")
	if err != nil || len(batch.Events) != 1 {
		t.Fatalf("poll: %+v %v", batch, err)
	}
	event := batch.Events[0]
	if event.Conversation.ExternalID != "group:456" || event.Conversation.Title != "Team" || event.Observation.Trigger != "mention" || event.Observation.ReplyID != "-42" || event.Observation.Text != "@999 hello[forward]" || event.Observation.Sender.Name != "A" || len(event.Observation.Attachments) != 1 {
		t.Fatalf("normalize: %+v", event)
	}
	replay, err := n.Poll(ctx, "")
	if err != nil || replay.Events[0].Observation.ID != event.Observation.ID {
		t.Fatal("pending websocket event was lost")
	}
	deleted, err := n.Poll(ctx, batch.NextCursor)
	if err != nil || deleted.Events[0].Observation.Kind != "message.deleted" || deleted.Events[0].Observation.MessageID != "-123" {
		t.Fatalf("recall event: %+v %v", deleted, err)
	}
}

func TestNapCatPrivateSentAndTemporaryGroupChatStayPrivate(t *testing.T) {
	n := &napcat{connection: domain.Connection{Meta: domain.Meta{ID: "n"}, AccountID: "999"}}
	for _, test := range []struct {
		raw, target string
		self        bool
	}{
		{`{"post_type":"message_sent","message_type":"private","self_id":999,"user_id":999,"target_id":123,"message_id":1,"message":[{"type":"text","data":{"text":"sent"}}]}`, "private:123", true},
		{`{"post_type":"message","message_type":"private","sub_type":"group","self_id":999,"user_id":123,"group_id":456,"message_id":2,"message":[{"type":"text","data":{"text":"temporary"}}]}`, "private:123", false},
	} {
		event, err := n.normalize(json.RawMessage(test.raw))
		if err != nil || event.Conversation.ExternalID != test.target || event.Observation.Self != test.self {
			t.Fatalf("private event: %+v %v", event, err)
		}
		if test.self && event.Observation.Trigger != "" {
			t.Fatal("self event triggered a reply")
		}
	}
	_, err := n.normalize(json.RawMessage(`{"post_type":"message","message_type":"private","self_id":888,"user_id":1,"message_id":2,"message":[]}`))
	if err == nil {
		t.Fatal("accepted events from a changed login account")
	}
	_, err = n.normalize(json.RawMessage(`{"post_type":"message","message_type":"private","self_id":999,"user_id":1,"message_id":2,"message":"[CQ:at,qq=all]"}`))
	if err == nil {
		t.Fatal("silently accepted CQ string instead of configured array")
	}
}

func TestNapCatValidateRejectsWrongWebSocketAccountAndAuthentication(t *testing.T) {
	for _, frame := range []string{
		`{"post_type":"meta_event","meta_event_type":"lifecycle","self_id":888}`,
		`{"status":"failed","retcode":1403,"message":"token validation failed"}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/get_login_info":
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"user_id":999,"nickname":"Astral"}}`)
			case "/get_status":
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"online":true}}`)
			case "/ws":
				socket, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer socket.CloseNow()
				_ = wsjson.Write(r.Context(), socket, json.RawMessage(frame))
				_, _, _ = socket.Read(r.Context())
			}
		}))
		n := napcatTestAdapter(t, server)
		if _, err := n.Validate(context.Background()); err == nil {
			t.Fatal("HTTP account validation hid a wrong or unauthenticated WebSocket account")
		}
		server.Close()
	}
}

func TestNapCatSendPreservesLiteralTextAndSingleMediaReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_group_msg" {
			t.Errorf("wrong send route: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Error("missing authorization")
		}
		var body struct {
			GroupID string          `json:"group_id"`
			Message []napcatSegment `json:"message"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		if body.GroupID != "456" || len(body.Message) != 3 || body.Message[0].Type != "reply" || body.Message[1].Type != "text" || body.Message[1].Data["text"] != "[CQ:at,qq=all]" {
			t.Errorf("unsafe message encoding: %+v", body)
		}
		encodedFile := strings.TrimPrefix(string(body.Message[2].Data["file"]), "base64://")
		data, _ := base64.StdEncoding.DecodeString(encodedFile)
		if string(data) != "report" || body.Message[2].Data["name"] != "report.txt" {
			t.Error("file did not pass through Computer stream")
		}
		fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"message_id":-100}}`)
	}))
	defer server.Close()
	receipt, err := napcatTestAdapter(t, server).Send(context.Background(), Send{Target: domain.Conversation{ExternalID: "group:456"}, Text: "[CQ:at,qq=all]", ReplyTo: "-42", File: io.NopCloser(strings.NewReader("report")), FileName: "report.txt", MediaType: "text/plain"})
	if err != nil || len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "-100" {
		t.Fatalf("send: %+v %v", receipt, err)
	}
}

func TestNapCatDownloadRemoteHostBase64AndRejectHostLocalPaths(t *testing.T) {
	for _, test := range []struct {
		data    string
		success bool
	}{
		{`{"file":"/napcat/private/file","url":"/napcat/private/file","file_size":"4","base64":"ZGF0YQ=="}`, true},
		{`{"file":"/napcat/private/file","url":"/napcat/private/file","file_size":"4"}`, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":`+test.data+`}`)
		}))
		n := napcatTestAdapter(t, server)
		file, err := n.Download(context.Background(), domain.Attachment{ExternalID: `{"file":"opaque-id"}`, Name: "file.txt"})
		if test.success {
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(file.Body)
			file.Body.Close()
			if string(data) != "data" {
				t.Fatal("file content mismatch")
			}
		} else if err == nil || !strings.Contains(err.Error(), "enableLocalFile2Url") {
			t.Fatalf("host-local path must not be read: %v", err)
		}
		server.Close()
	}
}

func TestNapCatSendReceiptErrorsAndLimits(t *testing.T) {
	for _, test := range []struct {
		raw     string
		unknown bool
	}{
		{`{"status":"failed","retcode":1400}`, false},
		{`{"status":"failed","retcode":1200}`, true},
		{`{"status":"failed","retcode":200,"message":"operation timed out"}`, true},
		{`{"status":"ok","retcode":0,"data":{}}`, true},
		{`broken`, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, test.raw) }))
		_, err := napcatTestAdapter(t, server).Send(context.Background(), Send{Target: domain.Conversation{ExternalID: "private:123"}, Text: "hello"})
		var sendErr *SendError
		if !errors.As(err, &sendErr) || sendErr.Unknown != test.unknown {
			t.Fatalf("receipt result: %v", err)
		}
		server.Close()
	}
	chunks, err := qqSplitText(strings.Repeat("你😀", 3000), 4000)
	if err != nil || len(chunks) != 2 || strings.Join(chunks, "") != strings.Repeat("你😀", 3000) {
		t.Fatal("Unicode split corrupted text")
	}
	if _, err := NewNapCat(domain.Connection{Config: encoded(NapCatConfig{BaseURL: "http://remote.example", WebSocketURL: "ws://remote.example"})}, "secret", nil); err == nil {
		t.Fatal("allowed cleartext remote credentials")
	}
}

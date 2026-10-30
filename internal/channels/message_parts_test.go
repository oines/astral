package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

type partResourceFixture struct {
	p          *testkit.Platform
	s          *Service
	account    domain.Connection
	chat       domain.Conversation
	caller     domain.ExecutionBinding
	server     *httptest.Server
	parent     json.RawMessage
	forward    json.RawMessage
	parentChat string
	replyID    string
	replyTo    string
	getMessage atomic.Int64
	getForward atomic.Int64
	getMedia   atomic.Int64
	hook       func()
}

func newPartResourceFixture(t *testing.T, computers int) *partResourceFixture {
	t.Helper()
	f := &partResourceFixture{p: testkit.New(t, computers, func(context.Context, int, map[string]any) any { return testkit.Text("unused") }), parentChat: "456"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_msg":
			f.getMessage.Add(1)
			var input map[string]string
			_ = json.NewDecoder(r.Body).Decode(&input)
			if f.replyID != "" && input["message_id"] == f.replyID {
				fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":{"message_id":%s,"message_type":"group","group_id":%s,"user_id":7,"message":[{"type":"reply","data":{"id":%s}}]}}`, f.replyID, f.parentChat, f.replyTo)
				return
			}
			if input["message_id"] != "42" {
				t.Errorf("unexpected parent message lookup: %+v", input)
			}
			fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":{"message_id":42,"message_type":"group","group_id":%s,"user_id":7,"message":%s}}`, f.parentChat, f.parent)
		case "/get_forward_msg":
			f.getForward.Add(1)
			var input map[string]string
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input["message_id"] != "current-handle" {
				t.Errorf("forward lookup did not use the currently verified parent handle: %+v", input)
			}
			if f.hook != nil {
				f.hook()
			}
			fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":%s}`, f.forward)
		case "/media":
			f.getMedia.Add(1)
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, "original")
		default:
			t.Errorf("unexpected resource endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	f.s = New(f.p.Store, f.p.Harness)
	f.account = domain.Connection{Meta: domain.Meta{ID: "parts-account"}, AgentID: f.p.Agent.ID, Adapter: "qq.napcat", Platform: "qq", AccountID: "999", Enabled: true, AccessMode: "all",
		Config: encoded(NapCatConfig{BaseURL: f.server.URL, WebSocketURL: strings.Replace(f.server.URL, "http:", "ws:", 1) + "/ws"})}
	if err := f.p.Store.Save(f.p.Context, "connections", &f.account.Meta, &f.account); err != nil {
		t.Fatal(err)
	}
	f.chat = domain.Conversation{Meta: domain.Meta{ID: "parts-chat"}, ConnectionID: f.account.ID, ExternalID: "group:456", Kind: "group", Title: "Allowed"}
	if err := f.p.Store.Save(f.p.Context, "conversations", &f.chat.Meta, &f.chat); err != nil {
		t.Fatal(err)
	}
	f.caller = domain.ExecutionBinding{AgentID: f.p.Agent.ID, SessionID: f.p.Session.ID}
	if computers != 0 {
		var err error
		f.caller, err = f.p.Hub.Bind(f.p.Context, f.p.Agent.ID, f.p.Session.ID, f.p.Agent.DefaultComputerID, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *partResourceFixture) archive(t *testing.T, parts []domain.MessagePart) domain.Observation {
	t.Helper()
	event := domain.Observation{SchemaVersion: 1, ID: domain.ID("event"), ConnectionID: f.account.ID, ConversationID: f.chat.ID, MessageID: "42", Kind: "message.created", ExternalEventID: domain.ID("external"), Parts: parts, SentAt: time.Now(), ReceivedAt: time.Now()}
	if _, err := f.p.Store.Archive(f.p.Context, event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestMessagePartsInlinePaginationAndNestedOriginalAreReadOnDemand(t *testing.T) {
	f := newPartResourceFixture(t, 1)
	f.parent = encoded([]any{map[string]any{"type": "forward", "data": map[string]any{"content": []any{
		map[string]any{"sender": map[string]any{"user_id": 7, "nickname": "Alice"}, "message": []any{map[string]any{"type": "text", "data": map[string]any{"text": "first"}}}},
		map[string]any{"sender": map[string]any{"user_id": 8, "nickname": "Bob"}, "group_id": 789, "message": []any{
			map[string]any{"type": "text", "data": map[string]any{"text": "second"}},
			map[string]any{"type": "image", "data": map[string]any{"file": "secret-file", "url": f.server.URL + "/media?signature=hidden", "name": "original.png", "file_size": 8}},
		}},
		map[string]any{"sender": map[string]any{"user_id": 9}, "message": []any{map[string]any{"type": "text", "data": map[string]any{"text": "third"}}}},
	}}}})
	parts, _, _ := napcatParts(f.parent, "42", "")
	event := f.archive(t, parts)
	root, err := f.s.MessageParts(f.p.Context, f.caller, f.chat.ID, "42")
	if err != nil || len(root) != 1 || root[0].Forward.Total != 3 || f.getMedia.Load() != 0 {
		t.Fatalf("root discovery downloaded media or lost structure: %+v %v", root, err)
	}
	page, err := f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0", 1, 1)
	if err != nil || page.NextOffset == nil || *page.NextOffset != 2 || page.Part.Forward.Total != 3 || page.Part.Forward.Messages[0].Parts[0].Ref != "p0.m1.p0" {
		t.Fatalf("unstable forward page: %+v %v", page, err)
	}
	attachment, err := f.s.MessagePartAttachment(f.p.Context, f.caller, f.chat.ID, "42", "p0.m1.p1")
	if err != nil || attachment.Ref != "p0.m1.p1" || !strings.Contains(attachment.ExternalID, "secret-file") {
		t.Fatalf("nested attachment identity lost: %+v %v", attachment, err)
	}
	content, err := f.s.MessagePartContent(f.p.Context, f.caller, f.chat.ID, "42", "p0.m1.p1")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(content.Body)
	_ = content.Body.Close()
	if readErr != nil || string(data) != "original" {
		t.Fatalf("wrong original source: %q %v", data, readErr)
	}
	if _, err := f.s.DownloadMessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0.m1.p1", ""); err == nil || f.getMedia.Load() != 1 {
		t.Fatal("missing destination started an implicit file download")
	}
	destination := filepath.Join(f.p.Directory, "selected-original.png")
	file, err := f.s.DownloadMessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0.m1.p1", destination)
	if err != nil || file.ComputerID != f.caller.ComputerID {
		t.Fatalf("explicit download lost command Computer: %+v %v", file, err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "original" {
		t.Fatalf("explicit download did not write source bytes: %q %v", data, err)
	}
	stored, err := f.p.Store.Observation(f.p.Context, event.ID)
	if err != nil || string(encoded(stored.Parts)) != string(encoded(event.Parts)) || f.getForward.Load() != 0 || f.getMessage.Load() != 0 || f.p.Requests.Load() != 0 {
		t.Fatal("resource reads modified history, invoked a model or acquired inline forwards remotely")
	}
}

func TestMessagePartsRemoteHandleMustComeFromCurrentAuthorizedParent(t *testing.T) {
	f := newPartResourceFixture(t, 0)
	f.parent = json.RawMessage(`[{"type":"forward","data":{"id":"current-handle"}}]`)
	f.forward = json.RawMessage(`{"messages":[{"sender":{"user_id":7,"nickname":"Alice"},"group_id":789,"message":[{"type":"text","data":{"text":"quoted only"}}]}]}`)
	f.archive(t, []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{ExternalID: "stale-handle", Status: "unexpanded"}}})
	page, err := f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0", 0, 20)
	if err != nil || page.Part.Forward.Messages[0].Parts[0].Text != "quoted only" || page.Part.Forward.Status != "inline" || f.getMessage.Load() != 1 || f.getForward.Load() != 1 {
		t.Fatalf("authorized current handle was not resolved: %+v %v", page, err)
	}
	f.parentChat = "789"
	if _, err = f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0", 0, 20); err == nil || f.getForward.Load() != 1 {
		t.Fatal("remapped parent message gained access to a forward from another group")
	}
	if _, err = f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "current-handle", "p0", 0, 20); err == nil {
		t.Fatal("bare platform resource handle was accepted as an archived parent")
	}
}

func TestMessagePartsOldSourceReadDoesNotBackfillAndRechecksRevocation(t *testing.T) {
	f := newPartResourceFixture(t, 0)
	f.parent = json.RawMessage(`[{"type":"forward","data":{"id":"current-handle"}}]`)
	f.forward = json.RawMessage(`{"messages":[]}`)
	event := f.archive(t, nil)
	parts, err := f.s.MessageParts(f.p.Context, f.caller, f.chat.ID, "42")
	if err != nil || len(parts) != 1 || parts[0].Forward == nil {
		t.Fatalf("old source was not readable on demand: %+v %v", parts, err)
	}
	stored, err := f.p.Store.Observation(f.p.Context, event.ID)
	if err != nil || len(stored.Parts) != 0 {
		t.Fatal("on-demand read rewrote archived observation")
	}
	f.hook = func() {
		f.account.AccessMode = "specified"
		if err := f.p.Store.Save(f.p.Context, "connections", &f.account.Meta, &f.account); err != nil {
			t.Error(err)
		}
	}
	if _, err := f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0", 0, 20); err == nil {
		t.Fatal("chat revoked during remote expansion still returned source content")
	}
	before := f.getMessage.Load()
	if _, err := f.s.MessageParts(f.p.Context, f.caller, f.chat.ID, "42"); err == nil || f.getMessage.Load() != before {
		t.Fatal("unallowed chat reached the platform")
	}
	f.caller.AgentID = "other-bot"
	if _, err := f.s.MessageParts(f.p.Context, f.caller, f.chat.ID, "42"); err == nil {
		t.Fatal("another Bot accessed this account's message parts")
	}
}

func TestMessagePartsRejectTraversalAndOversizedResponses(t *testing.T) {
	f := newPartResourceFixture(t, 0)
	f.parent = json.RawMessage(`[{"type":"forward","data":{"id":"current-handle"}}]`)
	f.forward = encoded(map[string]any{"messages": []any{}, "padding": strings.Repeat("x", 4<<20)})
	f.archive(t, []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{ExternalID: "current-handle", Status: "unexpanded"}}})
	for _, ref := range []string{"", "../p0", "p01", "p0.m-1.p0", "p0.m0", "p0.m0.p0.m0.p0.m0.p0.m0.p0.m0.p0.m0.p0.m0.p0"} {
		if _, err := f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", ref, 0, 1); err == nil {
			t.Errorf("invalid nested part ref accepted: %q", ref)
		}
	}
	if f.getForward.Load() != 0 || f.getMessage.Load() != 0 {
		t.Fatal("invalid refs performed remote requests")
	}
	if _, err := f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0", 0, 20); err == nil {
		t.Fatal("oversized resource response exceeded the shared budget")
	}
	if _, err := f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0", -1, 1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if _, err := f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0", 0, 51); err == nil {
		t.Fatal("oversized forward page accepted")
	}
}

func TestMessagePartPrivateScopeProofConsumesSharedCallBudget(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input map[string]any
		_ = json.NewDecoder(r.Body).Decode(&input)
		if r.URL.Path != "/get_friend_msg_history" || input["user_id"] != "123" || input["count"] != float64(100) {
			t.Errorf("private source proof escaped peer scope: %s %+v", r.URL.Path, input)
		}
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0,"data":{"messages":[{"message_id":42}]}}`)
	}))
	defer server.Close()
	n := napcatTestAdapter(t, server)
	resolver := &messagePartResolver{chat: domain.Conversation{ExternalID: "private:123"}, callsLeft: 1, bytesLeft: 4096}
	message := json.RawMessage(`{"message_id":42,"message_type":"private","user_id":999}`)
	if err := resolver.napcatScope(context.Background(), n, message, "42"); err != nil || calls.Load() != 1 || resolver.callsLeft != 0 {
		t.Fatalf("private source proof did not share resource budget: %v calls=%d remaining=%d", err, calls.Load(), resolver.callsLeft)
	}
	if err := resolver.napcatScope(context.Background(), n, message, "42"); err == nil || calls.Load() != 1 {
		t.Fatal("private source proof exceeded exhausted call budget")
	}
}

func TestMessagePartsDeletedParentCannotBeResurrectedByRemoteRead(t *testing.T) {
	f := newPartResourceFixture(t, 0)
	f.parent = json.RawMessage(`[{"type":"forward","data":{"id":"current-handle"}}]`)
	f.forward = json.RawMessage(`{"messages":[]}`)
	f.archive(t, []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{ExternalID: "current-handle", Status: "unexpanded"}}})
	deleted := domain.Observation{SchemaVersion: 1, ID: domain.ID("event"), ConnectionID: f.account.ID, ConversationID: f.chat.ID, MessageID: "42", ExternalEventID: "recalled-42", Kind: "message.deleted", SentAt: time.Now(), ReceivedAt: time.Now()}
	if _, err := f.p.Store.Archive(f.p.Context, deleted); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.MessageParts(f.p.Context, f.caller, f.chat.ID, "42"); err == nil {
		t.Fatal("deleted parent still exposed root parts")
	}
	if _, err := f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0", 0, 20); err == nil {
		t.Fatal("deleted parent still allowed resource expansion")
	}
	if f.getMessage.Load() != 0 || f.getForward.Load() != 0 || f.getMedia.Load() != 0 {
		t.Fatal("deleted parent was revived through remote source reads")
	}
}

func TestMessagePartsUseOnlyCurrentSameChatReplyProofAndRespectDeletion(t *testing.T) {
	f := newPartResourceFixture(t, 0)
	f.replyID, f.replyTo = "88", "42"
	f.parent = json.RawMessage(`[{"type":"forward","data":{"id":"current-handle"}}]`)
	f.forward = json.RawMessage(`{"messages":[{"sender":{"user_id":7},"message":[{"type":"text","data":{"text":"source via reply"}}]}]}`)
	quoted := []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{ExternalID: "old-quoted-handle", Status: "unexpanded"}}}
	reply := domain.Observation{SchemaVersion: 1, ID: "quote-proof", ConnectionID: f.account.ID, ConversationID: f.chat.ID, MessageID: "88", ReplyID: "42", ReplyParts: quoted, Kind: "message.created", ExternalEventID: "reply-88", SentAt: time.Now(), ReceivedAt: time.Now()}
	if _, err := f.p.Store.Archive(f.p.Context, reply); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.Message(f.p.Context, f.chat.ID, "42"); err == nil {
		t.Fatal("fixture unexpectedly fabricated the pre-archive original")
	}
	page, err := f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0", 0, 10)
	if err != nil || page.Part.Forward.Messages[0].Parts[0].Text != "source via reply" || f.getMessage.Load() != 2 || f.getForward.Load() != 1 {
		t.Fatalf("same-chat reply failed to prove current source: %+v %v", page, err)
	}
	prepared, err := f.s.prepareForwardParts(f.p.Context, f.caller, f.chat.ID, "42", quoted, nil)
	if err != nil || len(prepared[0].Forward.Messages) != 1 {
		t.Fatalf("automatic quoted forward did not reuse reply proof: %+v %v", prepared, err)
	}
	other := domain.Conversation{Meta: domain.Meta{ID: "unrelated-parts-chat"}, ConnectionID: f.account.ID, ExternalID: "group:789", Kind: "group"}
	if err := f.p.Store.Save(f.p.Context, "conversations", &other.Meta, &other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.MessageParts(f.p.Context, f.caller, other.ID, "42"); err == nil {
		t.Fatal("reply proof authorized an unrelated chat")
	}
	before := f.getForward.Load()
	f.replyTo = "77"
	if _, err := f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0", 0, 10); err == nil || f.getForward.Load() != before {
		t.Fatal("changed current reply still granted access through its stale source handle")
	}
	f.replyTo = "42"
	deleted := domain.Observation{SchemaVersion: 1, ID: "deleted-quoted-original", ConnectionID: f.account.ID, ConversationID: f.chat.ID, MessageID: "42", Kind: "message.deleted", ExternalEventID: "deleted-source-42", SentAt: time.Now(), ReceivedAt: time.Now()}
	if _, err := f.p.Store.Archive(f.p.Context, deleted); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.MessageParts(f.p.Context, f.caller, f.chat.ID, "42"); err == nil {
		t.Fatal("reply proof overrode original deletion tombstone")
	}
}

func TestMessagePartRejectsDeletionDuringExpansionAndDeletedReplyAnchor(t *testing.T) {
	f := newPartResourceFixture(t, 0)
	f.parent = json.RawMessage(`[{"type":"forward","data":{"id":"current-handle"}}]`)
	f.forward = json.RawMessage(`{"messages":[{"sender":{"user_id":7},"message":[{"type":"text","data":{"text":"must not return after recall"}}]}]}`)
	parts := []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{ExternalID: "current-handle", Status: "unexpanded"}}}
	f.archive(t, parts)
	f.hook = func() {
		deleted := domain.Observation{SchemaVersion: 1, ID: "deleted-during-expansion", ConnectionID: f.account.ID, ConversationID: f.chat.ID, MessageID: "42", Kind: "message.deleted", ExternalEventID: "deleted-during-expansion", SentAt: time.Now(), ReceivedAt: time.Now()}
		if _, err := f.p.Store.Archive(f.p.Context, deleted); err != nil {
			t.Error(err)
		}
	}
	if _, err := f.s.MessagePart(f.p.Context, f.caller, f.chat.ID, "42", "p0", 0, 10); err == nil {
		t.Fatal("deleted source still returned forward data after remote read")
	}
	quote := domain.Observation{SchemaVersion: 1, ID: "other-quote", ConnectionID: f.account.ID, ConversationID: f.chat.ID, MessageID: "99", ReplyID: "77", ReplyParts: parts, Kind: "message.created", ExternalEventID: "quote-99", SentAt: time.Now(), ReceivedAt: time.Now()}
	if _, err := f.p.Store.Archive(f.p.Context, quote); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.MessageParts(f.p.Context, f.caller, f.chat.ID, "77"); err != nil {
		t.Fatalf("live same-chat quoted source was not readable: %v", err)
	}
	quote.ID, quote.ExternalEventID, quote.Kind, quote.ReplyParts = "quote-deletion", "deleted-quote-99", "message.deleted", nil
	if _, err := f.p.Store.Archive(f.p.Context, quote); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.MessageParts(f.p.Context, f.caller, f.chat.ID, "77"); err == nil {
		t.Fatal("deleted enclosing reply remained a live resource authorization")
	}
}

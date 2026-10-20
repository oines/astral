package channels

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sessions "github.com/oines/astral-core/session"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

type botFixture struct {
	server  *httptest.Server
	updates chan []telegramUpdate
	sends   atomic.Int64
	unknown atomic.Bool
	mu      sync.Mutex
	texts   []string
	targets []string
	uploads [][]byte
	png     []byte
}

func newBot(t *testing.T) *botFixture {
	t.Helper()
	b := &botFixture{updates: make(chan []telegramUpdate, 20)}
	b.png, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aK7kAAAAASUVORK5CYII=")
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/bottest-token/")
		reply := func(value any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": value})
		}
		switch method {
		case "getMe":
			reply(telegramUser{ID: 999, IsBot: true, Username: "astral_bot", FirstName: "Astral"})
		case "getWebhookInfo":
			reply(map[string]any{"url": ""})
		case "getChat":
			var body struct {
				ChatID string `json:"chat_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			var id int64
			_, _ = fmt.Sscan(body.ChatID, &id)
			reply(telegramChat{ID: id, Title: body.ChatID, Type: "supergroup", IsForum: true})
		case "getUpdates":
			select {
			case updates := <-b.updates:
				reply(updates)
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
				reply([]any{})
			}
		case "getFile":
			reply(map[string]any{"file_path": "photos/source.png", "file_size": len(b.png)})
		case "sendMessage", "sendDocument":
			n := b.sends.Add(1)
			if b.unknown.Load() {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			}
			var text, target string
			if method == "sendMessage" {
				var data map[string]any
				_ = json.NewDecoder(r.Body).Decode(&data)
				text, _ = data["text"].(string)
				target, _ = data["chat_id"].(string)
			} else {
				reader, err := r.MultipartReader()
				if err != nil {
					t.Error(err)
					http.Error(w, "invalid", 400)
					return
				}
				for {
					part, nextErr := reader.NextPart()
					if nextErr == io.EOF {
						break
					}
					if nextErr != nil {
						t.Error(nextErr)
						return
					}
					data, readErr := io.ReadAll(part)
					if readErr != nil {
						t.Error(readErr)
						return
					}
					switch part.FormName() {
					case "chat_id":
						target = string(data)
					case "caption":
						text = string(data)
					case "document":
						b.mu.Lock()
						b.uploads = append(b.uploads, data)
						b.mu.Unlock()
					}
				}
			}
			b.mu.Lock()
			b.texts = append(b.texts, text)
			b.targets = append(b.targets, target)
			b.mu.Unlock()
			reply(telegramMessage{ID: 9000 + n})
		default:
			if r.URL.Path == "/file/bottest-token/photos/source.png" {
				_, _ = w.Write(b.png)
				return
			}
			t.Errorf("unexpected Telegram method %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(b.server.Close)
	return b
}
func (b *botFixture) connection(t *testing.T, s *Service) domain.Connection {
	t.Helper()
	c := domain.Connection{Name: "test bot", Adapter: "telegram.bot", Enabled: true, Config: encoded(TelegramConfig{BaseURL: b.server.URL})}
	token := "test-token"
	if err := s.SaveConnection(context.Background(), &c, &token); err != nil {
		t.Fatal(err)
	}
	if len(c.Diagnostics) == 0 {
		t.Fatal("privacy limitation was hidden")
	}
	return c
}
func createBinding(t *testing.T, p *testkit.Platform, s *Service, c domain.Connection, chat, topic string) domain.ConversationBinding {
	t.Helper()
	conversation, err := s.ResolveConversation(p.Context, c.ID, chat, topic)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.Bind(p.Context, conversation.ID, p.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}
func startChannels(t *testing.T, s *Service, ctx context.Context) {
	t.Helper()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(cleanup); err != nil {
			t.Error(err)
		}
	})
}
func update(id int64, text string) telegramUpdate {
	return telegramUpdate{ID: id, Message: &telegramMessage{ID: 100 + id, Date: time.Now().Unix(), Text: text, From: &telegramUser{ID: 7, FirstName: "Ada"}, Chat: telegramChat{ID: -100, Type: "supergroup", Title: "Workspace"}}}
}

func TestTelegramImagesObservationsAndCommittedReplySurviveRecovery(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 1, func(ctx context.Context, n int, request map[string]any) any {
		body := string(encoded(request))
		if n == 1 {
			if !strings.Contains(body, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(bot.png)) || !strings.Contains(body, "astral-file://") || !strings.Contains(body, ".astral/attachments/") {
				t.Error("IM image lost native pixels, source URI or Computer attachment")
			}
			if strings.Count(body, "photo request") != 1 || !strings.Contains(body, "background context") {
				t.Error("observation/trigger was duplicated or prior passive context was lost")
			}
			reply := testkit.Tool("exec_command", map[string]any{"cmd": "printf tool-result", "login": false})
			msg := reply["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
			msg["content"], msg["reasoning_content"] = "I received the image.", "private reasoning"
			return reply
		}
		if n == 3 && !strings.Contains(body, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(bot.png)) {
			t.Error("offline continuation did not replay frozen image")
		}
		return testkit.Tool("no_reply", map[string]any{})
	})
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s)
	binding := createBinding(t, p, s, c, "-100", "")
	startChannels(t, s, p.Context)
	passive := update(1, "background context")
	bot.updates <- []telegramUpdate{passive}
	event1 := stableID("event", c.ID, "1")
	testkit.Await(t, p.Context, func() bool { d, err := s.Delivery(p.Context, event1); return err == nil && d.InputID != "" })
	if p.Requests.Load() != 0 {
		t.Fatal("passive group message activated the model")
	}
	trigger := update(2, "😀 @astral_bot photo request")
	trigger.Message.Entities = []telegramEntity{{Type: "mention", Offset: 3, Length: 11}}
	trigger.Message.Photo = []telegramFile{{ID: "small", Width: 1, Height: 1}, {ID: "image", Width: 10, Height: 10, Size: int64(len(bot.png)), MIME: "image/png", Name: "source.png"}}
	bot.updates <- []telegramUpdate{trigger}
	testkit.Await(t, p.Context, func() bool { return p.Requests.Load() >= 2 })
	view := p.Idle(t, binding.SessionID)
	if view.LastRun.EndReason != "no_reply" {
		t.Fatal("silent end did not persist")
	}
	testkit.Await(t, p.Context, func() bool { return bot.sends.Load() == 1 })
	testkit.Await(t, p.Context, func() bool {
		items, _ := list[domain.Outbox](p.Context, p.Store, "outbox")
		return len(items) == 1 && items[0].Status == "accepted"
	})
	bot.mu.Lock()
	text := strings.Join(bot.texts, "")
	bot.mu.Unlock()
	if text != "I received the image." {
		t.Fatalf("non-committed assistant content escaped: %q", text)
	}
	self := update(3, "I received the image.")
	self.Message.From = &telegramUser{ID: 999, IsBot: true}
	edit := update(4, "edited background")
	edit.EditedMessage, edit.Message = edit.Message, nil
	edit.EditedMessage.ID = 101
	edit.EditedMessage.EditDate = time.Now().Unix()
	bot.updates <- []telegramUpdate{trigger, self, edit}
	testkit.Await(t, p.Context, func() bool {
		events, _ := p.Store.Observations(p.Context, binding.ConversationID, 0, 100)
		return len(events) == 4
	})
	testkit.Await(t, p.Context, func() bool {
		d, err := s.Delivery(p.Context, stableID("event", c.ID, "4"))
		return err == nil && d.InputID != ""
	})
	if p.Requests.Load() != 2 {
		t.Fatal("duplicate, self echo or passive edit reactivated model")
	}
	if err := s.Close(p.Context); err != nil {
		t.Fatal(err)
	}
	restarted := New(p.Store, p.Harness)
	startChannels(t, restarted, p.Context)
	var copy attachmentCopy
	if err := p.Store.Get(p.Context, "inbox_files", stableID("attachment", p.Agent.ID, stableID("event", c.ID, "2"), "102:0"), &copy); err != nil {
		t.Fatal(err)
	}
	ref, err := p.Harness.Files.Get(p.Context, p.Agent.ID, copy.URI)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(ref.Path); err != nil {
		t.Fatal(err)
	}
	if err = p.Daemons[0].Close(p.Context); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "after-offline", Text: "Remember the earlier image"}); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, binding.SessionID)
	coreSession, _ := p.Harness.Core(p.Context, binding.SessionID)
	page, err := coreSession.History().Scan(p.Context, sessions.Query{Kind: sessions.EntryModelRequest, MaxBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	frozen := 0
	for _, entry := range page.Entries {
		for _, m := range entry.ModelRequest.Request.Messages {
			for _, part := range m.Content {
				if part.Type == "image" && len(part.Data) > 0 {
					frozen++
				}
			}
		}
	}
	if frozen != 3 {
		t.Fatalf("actual rollout inputs lost images: %d", frozen)
	}
	if err = restarted.collect(p.Context, binding); err != nil {
		t.Fatal(err)
	}
	if bot.sends.Load() != 1 {
		t.Fatal("restart or silent end resent old assistant text")
	}
}

func TestOutboundUnknownIdempotencyRebindingAndRestart(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s)
	binding := createBinding(t, p, s, c, "-100", "")
	request := SendRequest{IntentID: "one-send", ConversationID: binding.ConversationID, Text: "send once"}
	out, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, request)
	if err != nil {
		t.Fatal(err)
	}
	bot.unknown.Store(true)
	if err = s.dispatch(p.Context, out[0].ID); err != nil {
		t.Fatal(err)
	}
	out, err = s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Status != "unknown" || bot.sends.Load() != 1 {
		t.Fatalf("ambiguous send replayed or lost: %+v", out)
	}
	if err = s.Retry(p.Context, out[0].ID); err == nil {
		t.Fatal("unknown send allowed blind retry")
	}
	changed := request
	changed.Text = "different"
	if _, err = s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, changed); err == nil {
		t.Fatal("same intent accepted changed content")
	}
	pending, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, SendRequest{IntentID: "old-binding", Text: "old target"})
	if err != nil {
		t.Fatal(err)
	}
	other := p.Agent
	other.Meta = domain.Meta{}
	other.Name = "new owner"
	if err = p.Harness.SaveAgent(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	newBinding, err := s.Bind(p.Context, binding.ConversationID, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newBinding.SessionID == binding.SessionID {
		t.Fatal("reassignment reused rollout")
	}
	if err = s.dispatch(p.Context, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	var blocked domain.Outbox
	if err = p.Store.Get(p.Context, "outbox", pending[0].ID, &blocked); err != nil {
		t.Fatal(err)
	}
	if blocked.Status != "blocked" || blocked.BindingID != binding.ID || bot.sends.Load() != 1 {
		t.Fatal("old outbound intent redirected")
	}
	// Simulate a crash after committing dispatch and before committing receipt.
	unknown := out[0]
	unknown.Status = "dispatching"
	if err = p.Store.Save(p.Context, "outbox", &unknown.Meta, &unknown); err != nil {
		t.Fatal(err)
	}
	startChannels(t, s, p.Context)
	testkit.Await(t, p.Context, func() bool {
		var got domain.Outbox
		return p.Store.Get(p.Context, "outbox", unknown.ID, &got) == nil && got.Status == "unknown"
	})
	if bot.sends.Load() != 1 {
		t.Fatal("restart repeated an unacknowledged send")
	}
}

func TestFileSendRetainsSourceAndTextFragmentsStayOrdered(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 2, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s)
	binding := createBinding(t, p, s, c, "-100", "22")
	b, err := p.Hub.Bind(p.Context, p.Agent.ID, binding.SessionID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("source Computer A\n"), 100000)
	ref, err := p.Harness.Files.Upload(p.Context, b, "report.txt", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, SendRequest{IntentID: "file", FileURI: filestream.URL(ref), Text: "report"})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Harness.SelectComputer(p.Context, binding.SessionID, p.Daemons[1].Credentials.ComputerID, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.dispatch(p.Context, out[0].ID); err != nil {
		t.Fatal(err)
	}
	bot.mu.Lock()
	uploaded := bot.uploads[0]
	bot.mu.Unlock()
	if sha256.Sum256(uploaded) != sha256.Sum256(data) {
		t.Fatal("outbound stream read a different source")
	}
	text := strings.Repeat("🌏", 3000) + " [report](astral-file://computer/secret)"
	fragments, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, SendRequest{IntentID: "long", Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 2 {
		t.Fatalf("UTF-16 limits not respected: %d", len(fragments))
	}
	startChannels(t, s, p.Context)
	testkit.Await(t, p.Context, func() bool {
		records, _ := s.group(p.Context, fragments[0].GroupID)
		return len(records) == 2 && records[0].Status == "accepted" && records[1].Status == "accepted"
	})
	bot.mu.Lock()
	texts := append([]string(nil), bot.texts...)
	bot.mu.Unlock()
	if strings.Join(texts[1:], "") != presentText(text) || strings.Contains(strings.Join(texts, ""), "astral-file://") {
		t.Fatal("fragment order or internal file presentation was wrong")
	}
}

func TestTelegramDoesNotLeakTokenAndRejectsActiveWebhook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getMe") {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": telegramUser{ID: 1, IsBot: true}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]string{"url": "https://example.test/webhook"}})
	}))
	defer server.Close()
	adapter, err := NewTelegram(domain.Connection{Config: encoded(TelegramConfig{BaseURL: server.URL})}, "private-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.Validate(context.Background()); err == nil || !strings.Contains(err.Error(), "webhook") {
		t.Fatalf("active webhook was not diagnosed: %v", err)
	}
	server.Close()
	if _, err = adapter.Validate(context.Background()); err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("transport exposed credential: %v", err)
	}
}

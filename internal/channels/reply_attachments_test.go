package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

func TestTelegramReplyMediaRetainsSourceMetadataWithoutCurrentAttachments(t *testing.T) {
	for _, test := range []struct{ name, source, kind, filename string }{
		{"photo", `"photo":[{"file_id":"photo","width":50,"height":20}]`, "image", "photo-42.jpg"},
		{"document", `"document":{"file_id":"document","file_name":"notes.txt","mime_type":"text/plain"}`, "file", "notes.txt"},
		{"audio", `"audio":{"file_id":"audio","file_name":"audio.mp3","mime_type":"audio/mpeg"}`, "audio", "audio.mp3"},
		{"video", `"video":{"file_id":"video","file_name":"video.mp4","mime_type":"video/mp4"}`, "video", "video.mp4"},
		{"sticker", `"sticker":{"file_id":"sticker","type":"regular","width":512,"height":512}`, "sticker", "sticker-42.webp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var message telegramMessage
			if err := json.Unmarshal([]byte(`{"message_id":43,"chat":{"id":-100,"type":"supergroup"},"text":"reply","reply_to_message":{"message_id":42,"chat":{"id":-100,"type":"supergroup"},"caption":"source caption",`+test.source+`}}`), &message); err != nil {
				t.Fatal(err)
			}
			n := &telegram{}
			event := n.normalize(1, message, false).Observation
			if len(event.Attachments) != 0 || len(event.ReplyAttachments) != 1 || event.ReplyAttachments[0].Ref != "42:0" || event.ReplyAttachments[0].Kind != test.kind || event.ReplyAttachments[0].Name != test.filename || event.ReplyPreview != "source caption" {
				t.Fatalf("quoted media lost source identity: %+v", event.ReplyAttachments)
			}
			message.Reply.Chat.ID = -200
			if len(n.normalize(2, message, false).Observation.ReplyAttachments) != 0 {
				t.Fatal("a foreign chat's media acquired a local download path")
			}
		})
	}
}

type replyDownloadCounter struct {
	Adapter
	downloads *atomic.Int64
}

func (a replyDownloadCounter) Download(ctx context.Context, attachment domain.Attachment) (Download, error) {
	a.downloads.Add(1)
	return a.Adapter.Download(ctx, attachment)
}

func TestTelegramReplyMediaDownloadsOnlyOnExplicitAuthorizedRequest(t *testing.T) {
	bot := newBot(t)
	var downloads atomic.Int64
	p := testkit.New(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		body := string(encoded(request["messages"]))
		if !strings.Contains(body, `message-id=\"42\"`) || !strings.Contains(body, `file-id=\"42:0\"`) || strings.Contains(body, `"type":"image_url"`) {
			t.Error("referenced photo was lost or automatically attached as model pixels")
		}
		return testkit.Text("received")
	})
	service := New(p.Store, p.Harness)
	service.Registry["telegram.bot"] = func(c domain.Connection, token string, client *http.Client) (Adapter, error) {
		base, err := NewTelegram(c, token, client)
		return replyDownloadCounter{Adapter: base, downloads: &downloads}, err
	}
	account := bot.connection(t, service, p.Agent.ID)
	binding := createBinding(t, p, service, account, "-100", "")
	startChannels(t, service, p.Context)
	incoming := update(1, "@astral_bot look at that photo")
	incoming.Message.Entities = []telegramEntity{{Type: "mention", Length: 11}}
	if err := json.Unmarshal([]byte(`{"message_id":42,"chat":{"id":-100,"type":"supergroup"},"from":{"id":7,"first_name":"Alice"},"photo":[{"file_id":"source-file","width":10,"height":10}],"caption":"source caption"}`), &incoming.Message.Reply); err != nil {
		t.Fatal(err)
	}
	bot.updates <- []telegramUpdate{incoming}
	testkit.Await(t, p.Context, func() bool { return p.Requests.Load() == 1 })
	p.Idle(t, binding.SessionID)
	if downloads.Load() != 0 {
		t.Fatal("reply delivery automatically downloaded the referenced source")
	}
	if _, err := p.Store.Message(p.Context, binding.ConversationID, "42"); err == nil {
		t.Fatal("embedded reply fabricated an original message in the archive")
	}
	event, err := p.Store.Message(p.Context, binding.ConversationID, "101")
	if err != nil || len(event.Attachments) != 0 || len(event.ReplyAttachments) != 1 {
		t.Fatal("archive mixed current and referenced attachments:", err)
	}
	execution := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: binding.SessionID}
	for _, bad := range []struct{ chat, message string }{{binding.ConversationID, "101"}, {"foreign-chat", "42"}} {
		if _, _, err := service.AttachmentContent(p.Context, execution, bad.chat, bad.message, "42:0"); err == nil {
			t.Fatal("reference was downloadable under the wrong message or chat")
		}
	}
	download, _, err := service.AttachmentContent(p.Context, execution, binding.ConversationID, "42", "42:0")
	if err != nil {
		t.Fatal("explicit original download failed:", err)
	}
	data, readErr := io.ReadAll(download.Body)
	closeErr := download.Body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(data, bot.png) || downloads.Load() != 1 {
		t.Fatal("explicit download changed original bytes or downloaded more than requested")
	}
}

func TestNapCatReplyMediaResolvesOnlyExistingSameChatArchive(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			t.Error("quoted media caused an unrequested network lookup/download")
			http.NotFound(w, r)
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		for _, id := range []int{43, 44} {
			_ = wsjson.Write(r.Context(), socket, json.RawMessage(fmt.Sprintf(`{"post_type":"message","message_type":"group","self_id":999,"group_id":456,"user_id":123,"message_id":%d,"message":[{"type":"reply","data":{"id":"%d"}},{"type":"text","data":{"text":"reply"}}]}`, id, id-1)))
		}
		_, _, _ = socket.Read(r.Context())
	}))
	t.Cleanup(server.Close)
	n := napcatTestAdapter(t, server)
	n.setStore(store)
	source := domain.Observation{SchemaVersion: 1, ID: "original", ExternalEventID: "original", Kind: "message.created", ConnectionID: n.connection.ID, ConversationID: stableID("conversation", n.connection.ID, "group:456", ""), MessageID: "42", Text: "original photo", Sender: domain.Sender{Name: "Alice"}, Attachments: []domain.Attachment{{Ref: "42:0", Kind: "image", Name: "original.png", ExternalID: "opaque-source"}}}
	if _, err := store.Archive(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	foreign := source
	foreign.ID, foreign.ExternalEventID, foreign.ConversationID, foreign.MessageID = "foreign", "foreign", "another-chat", "43"
	if _, err := store.Archive(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, err := n.Poll(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	event := first.Events[0].Observation
	if len(event.Attachments) != 0 || len(event.ReplyAttachments) != 1 || event.ReplyAttachments[0] != source.Attachments[0] || event.ReplySender != "Alice" || event.ReplyPreview != source.Text {
		t.Fatal("same-chat quoted media lost its original metadata")
	}
	body := messages.Observation(event, n.connection, first.Events[0].Conversation, nil, "").Content[0].Text
	if !strings.Contains(body, messages.AttachmentDownloadPath(source.ConversationID, "42", "42:0")) {
		t.Fatal("NapCat reference did not expose the original download path")
	}
	next, err := n.Poll(ctx, first.NextCursor)
	if err != nil || len(next.Events[0].Observation.ReplyAttachments) != 0 || !strings.Contains(next.Events[0].Observation.ReplyPreview, "unavailable") {
		t.Fatal("missing source was invented or resolved from a different chat")
	}
}

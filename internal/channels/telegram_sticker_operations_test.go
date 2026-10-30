package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/testkit"
)

type stickerOperationRequest struct {
	parameters map[string]any
	files      map[string][]byte
	filenames  map[string]string
}

type stickerOperationBot struct {
	server   *httptest.Server
	format   string
	unknown  atomic.Bool
	mu       sync.Mutex
	requests []stickerOperationRequest
}

func newStickerOperationBot(t *testing.T, format string) *stickerOperationBot {
	t.Helper()
	bot := &stickerOperationBot{format: format}
	bot.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
		}
		switch strings.TrimPrefix(r.URL.Path, "/bottest-token/") {
		case "getMe":
			reply(map[string]any{"id": 999, "is_bot": true, "username": "astral_bot", "first_name": "Astral"})
		case "getWebhookInfo":
			reply(map[string]any{"url": ""})
		case "getChat":
			var parameters map[string]any
			if err := json.NewDecoder(r.Body).Decode(&parameters); err != nil {
				t.Error(err)
				return
			}
			id, err := strconv.ParseInt(telegramID(parameters["chat_id"]), 10, 64)
			if err != nil {
				t.Error(err)
				return
			}
			reply(map[string]any{"id": id, "type": "supergroup", "title": "Sticker test", "is_forum": true})
		case "sendSticker":
			request := stickerOperationRequest{parameters: map[string]any{}, files: map[string][]byte{}, filenames: map[string]string{}}
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") {
				reader, err := r.MultipartReader()
				if err != nil {
					t.Error(err)
					return
				}
				for {
					part, err := reader.NextPart()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Error(err)
						return
					}
					data, err := io.ReadAll(part)
					if err != nil {
						t.Error(err)
						return
					}
					if part.FileName() == "" {
						request.parameters[part.FormName()] = string(data)
					} else {
						request.files[part.FormName()] = data
						request.filenames[part.FormName()] = part.FileName()
					}
				}
			} else if err := json.NewDecoder(r.Body).Decode(&request.parameters); err != nil {
				t.Error(err)
				return
			}
			bot.mu.Lock()
			bot.requests = append(bot.requests, request)
			messageID := 9000 + len(bot.requests)
			bot.mu.Unlock()
			if bot.unknown.Load() {
				// The platform consumed the request, but its response never arrives.
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
			chatID, _ := strconv.ParseInt(telegramID(request.parameters["chat_id"]), 10, 64)
			threadID, _ := strconv.ParseInt(telegramID(request.parameters["message_thread_id"]), 10, 64)
			reply(map[string]any{
				"message_id": messageID, "message_thread_id": threadID, "is_topic_message": threadID != 0, "date": 1760000000,
				"chat": map[string]any{"id": chatID, "type": "supergroup", "title": "Sticker test", "is_forum": true},
				"from": map[string]any{"id": 999, "is_bot": true, "username": "astral_bot", "first_name": "Astral"},
				"sticker": map[string]any{
					"file_id": "sent-" + bot.format, "file_unique_id": "unique-" + bot.format,
					"type": "regular", "width": 512, "height": 384, "file_size": 1234,
					"is_animated": bot.format == "animated", "is_video": bot.format == "video",
					"emoji": "🦊", "set_name": "AstralPack",
					"thumbnail": map[string]any{"file_id": "preview-" + bot.format, "file_unique_id": "preview-unique", "width": 128, "height": 96, "file_size": 42},
				},
			})
		default:
			t.Errorf("unexpected Telegram endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(bot.server.Close)
	return bot
}

func (b *stickerOperationBot) connection(t *testing.T, service *Service, agentID string) domain.Connection {
	t.Helper()
	connection := domain.Connection{Name: "Sticker bot", AgentID: agentID, Adapter: "telegram.bot", Enabled: true, Config: encoded(TelegramConfig{BaseURL: b.server.URL})}
	token := "test-token"
	if err := service.SaveConnection(t.Context(), &connection, &token); err != nil {
		t.Fatal(err)
	}
	return connection
}

func stickerOperationPlatform(t *testing.T, computers int, format string) (*testkit.Platform, *Service, *stickerOperationBot, domain.Connection, domain.ConversationBinding) {
	t.Helper()
	p := testkit.New(t, computers, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	bot := newStickerOperationBot(t, format)
	connection := bot.connection(t, s, p.Agent.ID)
	chat := createBinding(t, p, s, connection, "-100", "22")
	return p, s, bot, connection, chat
}

func assertStickerOperationArchive(t *testing.T, p *testkit.Platform, chat domain.ConversationBinding, format string, id int) {
	t.Helper()
	message, err := p.Store.Message(p.Context, chat.ConversationID, strconv.Itoa(id))
	if err != nil {
		t.Fatal("successful sticker receipt was not archived:", err)
	}
	if !message.Self || message.Trigger != "" || message.ConversationID != chat.ConversationID || message.Kind != "message.created" {
		t.Fatalf("sticker receipt has incorrect ownership or trigger: %+v", message)
	}
	if len(message.Attachments) != 2 {
		t.Fatalf("original sticker and thumbnail were not both archived: %+v", message.Attachments)
	}
	original := message.Attachments[0]
	metadata := original.Sticker
	mime := map[string]string{"static": "image/webp", "animated": "application/x-tgsticker", "video": "video/webm"}[format]
	if original.Kind != "sticker" || original.ExternalID != "sent-"+format || original.MediaType != mime || original.Size != 1234 || metadata == nil {
		t.Fatalf("original sticker was lost or replaced by its thumbnail: %+v", original)
	}
	if metadata.Type != "regular" || metadata.Format != format || metadata.Emoji != "🦊" || metadata.SetName != "AstralPack" || metadata.UniqueID != "unique-"+format || metadata.Width != 512 || metadata.Height != 384 {
		t.Fatalf("sticker metadata did not survive the successful receipt archive: %+v", metadata)
	}
	thumbnail := message.Attachments[1]
	if metadata.ThumbnailRef == "" || metadata.ThumbnailRef != thumbnail.Ref || thumbnail.Ref == original.Ref || thumbnail.Kind != "image" || thumbnail.ExternalID != "preview-"+format || thumbnail.Sticker != nil {
		t.Fatalf("thumbnail is not a separate linked image: original=%+v thumbnail=%+v", original, thumbnail)
	}
}

func TestTelegramSendStickerFileIDArchivesEveryFormat(t *testing.T) {
	for _, format := range []string{"static", "animated", "video"} {
		t.Run(format, func(t *testing.T) {
			p, s, bot, connection, chat := stickerOperationPlatform(t, 0, format)
			catalog, err := s.AccountOperations(p.Context, p.Agent.ID, connection.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, operation := range catalog {
				found = found || operation.Name == "sendSticker"
			}
			if !found {
				t.Fatal("named operations discovery omitted sendSticker")
			}
			result, err := s.AccountOperation(p.Context, domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: chat.SessionID}, connection.ID, "sendSticker", ChatOperationRequest{
				ConversationID: chat.ConversationID, Parameters: map[string]any{"sticker": "reusable-" + format, "emoji": "🦊", "disable_notification": true},
			})
			if err != nil || !bytes.Contains(result.Result, []byte(`"message_id":9001`)) {
				t.Fatalf("named sendSticker failed: result=%s err=%v", result.Result, err)
			}
			bot.mu.Lock()
			defer bot.mu.Unlock()
			if len(bot.requests) != 1 {
				t.Fatalf("sendSticker requests=%d", len(bot.requests))
			}
			request := bot.requests[0]
			if request.parameters["sticker"] != "reusable-"+format || request.parameters["chat_id"] != "-100" || telegramID(request.parameters["message_thread_id"]) != "22" || request.parameters["emoji"] != "🦊" || len(request.files) != 0 {
				t.Fatalf("file_id or frozen chat/topic changed: %+v", request)
			}
			assertStickerOperationArchive(t, p, chat, format, 9001)
			files, err := p.Store.List(p.Context, "files")
			if err != nil || len(files) != 0 {
				t.Fatalf("file_id receipt automatically saved a Computer file: files=%d err=%v", len(files), err)
			}
		})
	}
}

func TestTelegramSendStickerMultipartReadsTargetComputerPathAndFileRef(t *testing.T) {
	p, s, bot, connection, chat := stickerOperationPlatform(t, 1, "static")
	tool, err := s.MessageTool(p.Context, p.Agent.ID, chat.ConversationID)
	if err != nil || tool.Name != "send_message" || tool.Version != "astral-4/telegram.bot" || strings.Contains(string(tool.Parameters), "sticker") {
		t.Fatal("named sticker support changed the send_message tool:", err)
	}
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, chat.SessionID, p.Daemons[0].Credentials.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	fs, err := (&computers.Backend{Hub: p.Hub}).FileSystem(p.Context, core.Invocation{SessionID: binding.SessionID, Binding: computers.BindingJSON(binding)})
	if err != nil {
		t.Fatal(err)
	}
	path, err := fs.ResolvePath(p.Context, "target-computer-sticker.webp", false)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("RIFF-test-WEBP-original-sticker-bytes")
	// Write through the authenticated Computer RPC, rather than a Server temp
	// file. Both uploads must resolve and read that Computer's file service.
	if err := fs.WriteFile(p.Context, path.Path, payload); err != nil {
		t.Fatal(err)
	}
	ref, err := p.Harness.Files.Reference(p.Context, binding, path.Path, true)
	if err != nil || ref.ComputerID != binding.ComputerID {
		t.Fatalf("Computer FileRef was not created: %+v %v", ref, err)
	}
	for index, location := range []string{path.Path, filestream.URL(ref)} {
		result, err := s.AccountOperation(p.Context, binding, connection.ID, "sendSticker", ChatOperationRequest{ConversationID: chat.ConversationID,
			Parameters: map[string]any{"emoji": "🦊"}, Files: map[string]string{"sticker": location}})
		if err != nil || len(result.Result) == 0 {
			t.Fatalf("Computer sticker upload failed: %s %v", result.Result, err)
		}
		bot.mu.Lock()
		request := bot.requests[index]
		bot.mu.Unlock()
		if !bytes.Equal(request.files["sticker"], payload) || request.filenames["sticker"] != "target-computer-sticker.webp" || request.parameters["sticker"] != "attach://sticker" || request.parameters["chat_id"] != "-100" || telegramID(request.parameters["message_thread_id"]) != "22" {
			t.Fatalf("multipart did not preserve source bytes and target scope: %+v", request)
		}
		assertStickerOperationArchive(t, p, chat, "static", 9001+index)
	}
	// FileRefs do not bypass current Computer permissions, even after a prior
	// upload succeeded and the reference is still present in the database.
	p.Agent.AllowedComputers = nil
	p.Agent.DefaultComputerID = ""
	if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AccountOperation(p.Context, binding, connection.ID, "sendSticker", ChatOperationRequest{ConversationID: chat.ConversationID,
		Files: map[string]string{"sticker": filestream.URL(ref)}}); err == nil {
		t.Fatal("sticker upload bypassed revoked Computer permission")
	}
	bot.mu.Lock()
	defer bot.mu.Unlock()
	if len(bot.requests) != 2 {
		t.Fatal("rejected Computer file upload reached Telegram")
	}
}

func TestTelegramSendStickerRejectsChatTopicAndBotScopeOverrides(t *testing.T) {
	p, s, bot, connection, chat := stickerOperationPlatform(t, 0, "static")
	forbidden, err := s.ResolveConversation(p.Context, connection.ID, "-200", "")
	if err != nil {
		t.Fatal(err)
	}
	binding := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: chat.SessionID}
	for _, test := range []struct {
		name       string
		binding    domain.ExecutionBinding
		chat       string
		parameters map[string]any
	}{
		{name: "foreign Bot", binding: domain.ExecutionBinding{AgentID: "another_bot", SessionID: chat.SessionID}, chat: chat.ConversationID},
		{name: "unbound chat", binding: binding, chat: forbidden.ID},
		{name: "chat override", binding: binding, chat: chat.ConversationID, parameters: map[string]any{"chat_id": "-200"}},
		{name: "topic override", binding: binding, chat: chat.ConversationID, parameters: map[string]any{"message_thread_id": float64(23)}},
		{name: "alternate business identity", binding: binding, chat: chat.ConversationID, parameters: map[string]any{"business_connection_id": "another_identity"}},
		{name: "reply chat override", binding: binding, chat: chat.ConversationID, parameters: map[string]any{"reply_parameters": map[string]any{"chat_id": "-200", "message_id": float64(7)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parameters := test.parameters
			if parameters == nil {
				parameters = map[string]any{}
			}
			parameters["sticker"] = "reusable-sticker"
			if _, err := s.AccountOperation(p.Context, test.binding, connection.ID, "sendSticker", ChatOperationRequest{ConversationID: test.chat, Parameters: parameters}); err == nil {
				t.Fatal("sendSticker escaped the authorized Bot/chat/topic")
			}
		})
	}
	bot.mu.Lock()
	defer bot.mu.Unlock()
	if len(bot.requests) != 0 {
		t.Fatal("unauthorized sendSticker reached Telegram")
	}
}

func TestTelegramSendStickerUnknownResponseIsNotRetriedOrArchived(t *testing.T) {
	p, s, bot, connection, chat := stickerOperationPlatform(t, 0, "static")
	bot.unknown.Store(true)
	result, err := s.AccountOperation(p.Context, domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: chat.SessionID}, connection.ID, "sendSticker", ChatOperationRequest{
		ConversationID: chat.ConversationID, Parameters: map[string]any{"sticker": "reusable-sticker"},
	})
	if err == nil || len(result.Result) != 0 {
		t.Fatalf("unknown sendSticker outcome was reported as success: %s %v", result.Result, err)
	}
	observations, err := p.Store.Observations(p.Context, chat.ConversationID, 0, 100)
	if err != nil || len(observations) != 0 {
		t.Fatalf("unknown outcome fabricated an archived receipt: %+v %v", observations, err)
	}
	bot.mu.Lock()
	defer bot.mu.Unlock()
	if len(bot.requests) != 1 {
		t.Fatalf("unknown sendSticker was repeated: requests=%d", len(bot.requests))
	}
}

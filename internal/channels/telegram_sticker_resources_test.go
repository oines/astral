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
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

type stickerResourceBot struct {
	server         *httptest.Server
	mu             sync.Mutex
	methods        []string
	beforeResource func() error
	beforeGetFile  func() error
	streamFile     func(http.ResponseWriter, *http.Request, telegramStickerTestFile) bool
	files          map[string]telegramStickerTestFile
}

func stickerResourcePlatform(t *testing.T, computerCount int) (*testkit.Platform, *Service, *stickerResourceBot, domain.Connection) {
	t.Helper()
	p := testkit.New(t, computerCount, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	bot := &stickerResourceBot{files: map[string]telegramStickerTestFile{
		"static-file":        {path: "stickers/static.webp", mediaType: "application/octet-stream", body: "static sticker bytes"},
		"animated-file":      {path: "stickers/animated.tgs", mediaType: "application/octet-stream", body: "animated sticker bytes"},
		"video-file":         {path: "stickers/video.webm", mediaType: "application/octet-stream", body: "video sticker bytes"},
		"animated-thumbnail": {path: "thumbnails/animated.jpg", mediaType: "image/jpeg", body: "animated thumbnail bytes"},
		"set-thumbnail":      {path: "thumbnails/set.webp", mediaType: "image/webp", body: "set thumbnail bytes"},
		"custom-file":        {path: "stickers/custom.tgs", mediaType: "application/octet-stream", body: "custom emoji bytes"},
		"custom-thumbnail":   {path: "thumbnails/custom.webp", mediaType: "image/webp", body: "custom emoji thumbnail bytes"},
	}}
	sticker := func(id, format, customID, thumbnail string) map[string]any {
		value := map[string]any{"file_id": id, "file_unique_id": "unique-" + id, "type": "regular", "width": 512, "height": 256, "emoji": "🦊", "is_animated": format == "animated", "is_video": format == "video"}
		if customID != "" {
			value["type"], value["custom_emoji_id"] = "custom_emoji", customID
		}
		if thumbnail != "" {
			value["thumbnail"] = map[string]any{"file_id": thumbnail, "width": 128, "height": 64}
		}
		return value
	}
	bot.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/botresource-test/")
		bot.mu.Lock()
		bot.methods = append(bot.methods, method)
		resourceHook, fileHook, streamHook := bot.beforeResource, bot.beforeGetFile, bot.streamFile
		bot.mu.Unlock()
		reply := func(value any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": value})
		}
		switch method {
		case "getMe":
			reply(map[string]any{"id": 999, "is_bot": true, "username": "resource_bot"})
		case "getWebhookInfo":
			reply(map[string]any{"url": ""})
		case "getChat":
			var parameters map[string]any
			_ = json.NewDecoder(r.Body).Decode(&parameters)
			id, err := strconv.ParseInt(telegramID(parameters["chat_id"]), 10, 64)
			if err != nil {
				t.Error(err)
				http.Error(w, "invalid chat ID", http.StatusBadRequest)
				return
			}
			reply(map[string]any{"id": id, "type": "supergroup", "title": "Resource test", "is_forum": true})
		case "getStickerSet", "getCustomEmojiStickers":
			if resourceHook != nil {
				if err := resourceHook(); err != nil {
					t.Error(err)
					http.Error(w, "resource hook failed", http.StatusInternalServerError)
					return
				}
			}
			if method == "getStickerSet" {
				reply(map[string]any{"name": "oines1", "title": "Sticker resource test", "thumbnail": map[string]any{"file_id": "set-thumbnail"}, "stickers": []any{
					sticker("static-file", "static", "", ""), sticker("animated-file", "animated", "", "animated-thumbnail"), sticker("video-file", "video", "", ""),
				}})
			} else {
				reply([]any{sticker("custom-file", "animated", "12345", "custom-thumbnail")})
			}
		case "getFile":
			if fileHook != nil {
				if err := fileHook(); err != nil {
					t.Error(err)
					http.Error(w, "file hook failed", http.StatusInternalServerError)
					return
				}
			}
			var parameters struct {
				FileID string `json:"file_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&parameters)
			file, ok := bot.files[parameters.FileID]
			if !ok {
				t.Errorf("unverified file_id reached Telegram getFile: %q", parameters.FileID)
				http.NotFound(w, r)
				return
			}
			reply(map[string]any{"file_path": file.path, "file_size": len(file.body)})
		default:
			for _, file := range bot.files {
				if r.URL.Path == "/file/botresource-test/"+file.path {
					if streamHook != nil && streamHook(w, r, file) {
						return
					}
					w.Header().Set("Content-Type", file.mediaType)
					_, _ = io.WriteString(w, file.body)
					return
				}
			}
			t.Errorf("unexpected Telegram resource method: %q", method)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(bot.server.Close)
	connection := domain.Connection{Name: "Sticker resources", AgentID: p.Agent.ID, Adapter: "telegram.bot", Enabled: true, Config: encoded(TelegramConfig{BaseURL: bot.server.URL})}
	secret := "resource-test"
	if err := s.SaveConnection(p.Context, &connection, &secret); err != nil {
		t.Fatal(err)
	}
	return p, s, bot, connection
}

func (bot *stickerResourceBot) calls(method string) int {
	bot.mu.Lock()
	defer bot.mu.Unlock()
	count := 0
	for _, name := range bot.methods {
		if name == method {
			count++
		}
	}
	return count
}

func stickerResourceCounts(t *testing.T, p *testkit.Platform) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, kind := range []string{"sessions", "files", "bindings", "conversations"} {
		items, err := p.Store.List(p.Context, kind)
		if err != nil {
			t.Fatal(err)
		}
		counts[kind] = len(items)
	}
	return counts
}

func assertStickerResourceCounts(t *testing.T, p *testkit.Platform, before map[string]int) {
	t.Helper()
	after := stickerResourceCounts(t, p)
	for kind, count := range before {
		if after[kind] != count {
			t.Fatalf("resource read created %s: before=%d after=%d", kind, count, after[kind])
		}
	}
}

func TestTelegramStickerResourceOperationsAreAccountScopedWithoutCreatingIMState(t *testing.T) {
	p, s, bot, account := stickerResourcePlatform(t, 0)
	b := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}
	before := stickerResourceCounts(t, p)
	catalog, err := s.AccountOperations(p.Context, b.AgentID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, operation := range catalog {
		resource := operation.Name == "getStickerSet" || operation.Name == "getCustomEmojiStickers"
		if operation.AccountScoped != resource {
			t.Fatalf("incorrect scope for %s: %#v", operation.Name, operation)
		}
		if operation.AccountScoped {
			marked++
		}
	}
	if marked != 2 {
		t.Fatal("resource operations missing from the catalog")
	}
	for _, request := range []struct {
		method string
		params map[string]any
	}{{"getStickerSet", map[string]any{"name": "oines1"}}, {"getCustomEmojiStickers", map[string]any{"custom_emoji_ids": []any{"12345"}}}} {
		result, err := s.AccountOperation(p.Context, b, account.ID, request.method, ChatOperationRequest{Parameters: request.params})
		if err != nil || !json.Valid(result.Result) || !bytes.Contains(result.Result, []byte("file_id")) || bytes.Contains(result.Result, []byte("resource-test")) {
			t.Fatalf("resource metadata request failed or exposed credentials: %s, %v", result.Result, err)
		}
	}
	if bot.calls("getFile") != 0 {
		t.Fatal("metadata query automatically downloaded a sticker")
	}
	assertStickerResourceCounts(t, p, before)
	// Supplying an allowed Topic is optional context for a resource read, not a
	// reason to run the parent-chat-only mutation restrictions.
	chat := createBinding(t, p, s, account, "-100", "22")
	before = stickerResourceCounts(t, p)
	if _, err = s.AccountOperation(p.Context, b, account.ID, "getStickerSet", ChatOperationRequest{ConversationID: chat.ConversationID, Parameters: map[string]any{"name": "oines1"}}); err != nil {
		t.Fatalf("allowed Topic blocked an account resource query: %v", err)
	}
	if _, err = s.AccountOperation(p.Context, b, account.ID, "setChatTitle", ChatOperationRequest{ConversationID: chat.ConversationID, Parameters: map[string]any{"title": "must remain blocked"}}); err == nil {
		t.Fatal("resource support broadened parent-chat mutation permissions")
	}
	observations, err := p.Store.Observations(p.Context, chat.ConversationID, 0, 100)
	if err != nil || len(observations) != 0 {
		t.Fatal("resource metadata was archived as a conversation message:", err)
	}
	assertStickerResourceCounts(t, p, before)
}

func TestTelegramStickerResourceRejectsUnsupportedInputsBeforeNetwork(t *testing.T) {
	p, s, bot, account := stickerResourcePlatform(t, 0)
	b := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}
	for _, test := range []struct {
		name, method string
		request      TelegramRequest
	}{
		{"missing name", "getStickerSet", TelegramRequest{}},
		{"name is URL", "getStickerSet", TelegramRequest{Parameters: map[string]any{"name": "https://t.me/addstickers/oines1"}}},
		{"name too long", "getStickerSet", TelegramRequest{Parameters: map[string]any{"name": strings.Repeat("a", 65)}}},
		{"unsupported parameter", "getStickerSet", TelegramRequest{Parameters: map[string]any{"name": "oines1", "chat_id": "-100"}}},
		{"source chat", "getStickerSet", TelegramRequest{SourceConversationID: "foreign", Parameters: map[string]any{"name": "oines1"}}},
		{"upload", "getStickerSet", TelegramRequest{Files: map[string]string{"sticker": "/must/not/read"}, Parameters: map[string]any{"name": "oines1"}}},
		{"emoji numeric", "getCustomEmojiStickers", TelegramRequest{Parameters: map[string]any{"custom_emoji_ids": []any{float64(12345)}}}},
		{"emoji empty", "getCustomEmojiStickers", TelegramRequest{Parameters: map[string]any{"custom_emoji_ids": []any{}}}},
		{"emoji too many", "getCustomEmojiStickers", TelegramRequest{Parameters: map[string]any{"custom_emoji_ids": strings.Fields(strings.Repeat("12345 ", 201))}}},
		{"emoji overflow", "getCustomEmojiStickers", TelegramRequest{Parameters: map[string]any{"custom_emoji_ids": []string{"18446744073709551616"}}}},
		{"emoji negative", "getCustomEmojiStickers", TelegramRequest{Parameters: map[string]any{"custom_emoji_ids": []string{"-1"}}}},
		{"emoji zero", "getCustomEmojiStickers", TelegramRequest{Parameters: map[string]any{"custom_emoji_ids": []string{"0"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.TelegramOperation(p.Context, b, account.ID, test.method, test.request); err == nil {
				t.Fatal("invalid resource request succeeded")
			}
		})
	}
	if bot.calls("getStickerSet") != 0 || bot.calls("getCustomEmojiStickers") != 0 || bot.calls("getFile") != 0 {
		t.Fatal("invalid resource input reached Telegram")
	}
}

func TestTelegramStickerResourceOptionalConversationMustBelongToAccount(t *testing.T) {
	p, s, bot, account := stickerResourcePlatform(t, 0)
	b := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}
	other := account
	other.Meta, other.AccessMode = domain.Meta{ID: "other-resource-account"}, "all"
	if err := p.Store.Save(p.Context, "connections", &other.Meta, &other); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []domain.Connection{account, other} {
		chat := domain.Conversation{Meta: domain.Meta{ID: "resource-chat-" + owner.ID}, ConnectionID: owner.ID, ExternalID: "-123", Kind: "group"}
		if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
			t.Fatal(err)
		}
		if _, err := s.TelegramOperation(p.Context, b, account.ID, "getStickerSet", TelegramRequest{ConversationID: chat.ID, Parameters: map[string]any{"name": "oines1"}}); err == nil {
			t.Fatal("unbound specified chat or another account's chat passed resource context validation")
		}
	}
	if bot.calls("getStickerSet") != 0 {
		t.Fatal("invalid optional conversation reached Telegram")
	}
}

func TestTelegramStickerContentRechecksMembershipAndStreamsWithoutSaving(t *testing.T) {
	p, s, bot, account := stickerResourcePlatform(t, 0)
	b := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}
	before := stickerResourceCounts(t, p)
	for _, test := range []struct {
		source TelegramStickerSource
		mime   string
	}{
		{TelegramStickerSource{SetName: "oines1", FileID: "static-file"}, "image/webp"},
		{TelegramStickerSource{SetName: "oines1", FileID: "animated-file"}, "application/x-tgsticker"},
		{TelegramStickerSource{SetName: "oines1", FileID: "video-file"}, "video/webm"},
		{TelegramStickerSource{SetName: "oines1", FileID: "animated-thumbnail"}, "image/jpeg"},
		{TelegramStickerSource{SetName: "oines1", FileID: "set-thumbnail"}, "image/webp"},
		{TelegramStickerSource{CustomEmojiID: "12345", FileID: "custom-file"}, "application/x-tgsticker"},
		{TelegramStickerSource{CustomEmojiID: "12345", FileID: "custom-thumbnail"}, "image/webp"},
	} {
		t.Run(test.source.FileID, func(t *testing.T) {
			download, err := s.TelegramStickerContent(p.Context, b, account.ID, test.source)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(download.Body)
			_ = download.Body.Close()
			if err != nil || string(body) != bot.files[test.source.FileID].body || download.MediaType != test.mime || strings.Contains(download.Name, "resource-test") {
				t.Fatalf("invalid resource content: %q %#v, %v", body, download, err)
			}
		})
	}
	if bot.calls("getStickerSet") != 5 || bot.calls("getCustomEmojiStickers") != 2 {
		t.Fatal("content downloads reused cached membership instead of rechecking official resources")
	}
	assertStickerResourceCounts(t, p, before)
	getFileBefore := bot.calls("getFile")
	for _, source := range []TelegramStickerSource{
		{SetName: "oines1", FileID: "unlisted-file"},
		{CustomEmojiID: "999", FileID: "custom-file"},
		{SetName: "oines1", CustomEmojiID: "12345", FileID: "static-file"},
		{FileID: "static-file"}, {SetName: "oines1"},
	} {
		if download, err := s.TelegramStickerContent(p.Context, b, account.ID, source); err == nil {
			_ = download.Body.Close()
			t.Fatal("arbitrary file ID/source escaped resource membership validation")
		}
	}
	if bot.calls("getFile") != getFileBefore {
		t.Fatal("unverified file ID reached Telegram getFile")
	}
}

func TestTelegramStickerResourcesRejectForeignDisabledOrRevokedAccounts(t *testing.T) {
	for _, phase := range []string{"foreign Bot", "disabled before resource", "disabled during resource", "disabled during getFile"} {
		t.Run(phase, func(t *testing.T) {
			p, s, bot, account := stickerResourcePlatform(t, 0)
			b := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}
			disable := func() error {
				account.Enabled = false
				return p.Store.Save(p.Context, "connections", &account.Meta, &account)
			}
			switch phase {
			case "foreign Bot":
				b.AgentID = "another-Bot"
			case "disabled before resource":
				if err := disable(); err != nil {
					t.Fatal(err)
				}
			case "disabled during resource":
				bot.mu.Lock()
				bot.beforeResource = disable
				bot.mu.Unlock()
			case "disabled during getFile":
				bot.mu.Lock()
				bot.beforeGetFile = disable
				bot.mu.Unlock()
			}
			source := TelegramStickerSource{SetName: "oines1", FileID: "static-file"}
			if download, err := s.TelegramStickerContent(p.Context, b, account.ID, source); err == nil {
				_ = download.Body.Close()
				t.Fatal("account permission loss did not block resource content")
			}
			if phase == "disabled during resource" && bot.calls("getFile") != 0 {
				t.Fatal("disabled resource account still started a file download")
			}
		})
	}
}

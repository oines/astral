package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

const gatewayStickerToken = "gateway-sticker-test-token"
const gatewayCustomEmojiID = "5368324170671202286"

type stickerResourceGateway struct {
	p          *testkit.Platform
	account    domain.Connection
	binding    domain.ExecutionBinding
	client     *http.Client
	original   []byte
	thumbnail  []byte
	fileCalls  atomic.Int64
	mediaCalls atomic.Int64
}

func newStickerResourceGateway(t *testing.T, computerCount int) *stickerResourceGateway {
	t.Helper()
	f := &stickerResourceGateway{
		p:         testkit.New(t, computerCount, func(context.Context, int, map[string]any) any { return testkit.Text("unused") }),
		original:  bytes.Repeat([]byte{0x1f, 0x8b, 0, 0x70, 0xff, 0x10, 0, 0x7f}, 8192),
		thumbnail: []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 0x4a, 0x46, 0x49, 0x46},
	}
	sticker := map[string]any{
		"file_id": "set-original", "file_unique_id": "set-unique", "type": "regular", "set_name": "AstralGatewayStickers",
		"width": 512, "height": 512, "is_animated": true, "is_video": false, "file_size": len(f.original),
		"thumbnail": map[string]any{"file_id": "set-preview", "file_unique_id": "preview-unique", "width": 128, "height": 128, "file_size": len(f.thumbnail)},
	}
	emoji := map[string]any{
		"file_id": "emoji-original", "file_unique_id": "emoji-unique", "type": "custom_emoji", "custom_emoji_id": gatewayCustomEmojiID,
		"width": 100, "height": 100, "is_animated": true, "is_video": false, "file_size": len(f.original),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
		}
		if strings.HasPrefix(r.URL.Path, "/file/bot"+gatewayStickerToken+"/") {
			f.mediaCalls.Add(1)
			switch strings.TrimPrefix(r.URL.Path, "/file/bot"+gatewayStickerToken+"/") {
			case "stickers/original.tgs":
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(f.original)
			case "thumbnails/preview.jpg":
				w.Header().Set("Content-Type", "image/jpeg")
				_, _ = w.Write(f.thumbnail)
			default:
				t.Error("unexpected Telegram file selection")
				http.NotFound(w, r)
			}
			return
		}
		method := strings.TrimPrefix(r.URL.Path, "/bot"+gatewayStickerToken+"/")
		switch method {
		case "getMe":
			reply(map[string]any{"id": 999, "is_bot": true, "username": "astral_gateway_bot"})
		case "getWebhookInfo":
			reply(map[string]any{"url": ""})
		case "getStickerSet", "getCustomEmojiStickers", "getFile":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				http.Error(w, "invalid fixture request", http.StatusBadRequest)
				return
			}
			if input["chat_id"] != nil || input["message_thread_id"] != nil {
				t.Error("resource lookup acquired a chat destination")
			}
			switch method {
			case "getStickerSet":
				if input["name"] == "broken" {
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "upstream rejected " + gatewayStickerToken})
					return
				}
				if input["name"] != "AstralGatewayStickers" {
					t.Error("unexpected sticker set lookup")
				}
				reply(map[string]any{"name": "AstralGatewayStickers", "title": "Astral", "sticker_type": "regular", "stickers": []any{sticker}})
			case "getCustomEmojiStickers":
				ids, _ := input["custom_emoji_ids"].([]any)
				if len(ids) != 1 || ids[0] != gatewayCustomEmojiID {
					t.Error("custom emoji lookup changed its string identifier")
				}
				reply([]any{emoji})
			case "getFile":
				f.fileCalls.Add(1)
				path, size := "stickers/original.tgs", len(f.original)
				switch input["file_id"] {
				case "set-original", "emoji-original":
				case "set-preview":
					path, size = "thumbnails/preview.jpg", len(f.thumbnail)
				default:
					t.Error("unselected file reached Telegram getFile")
				}
				reply(map[string]any{"file_path": path, "file_size": size})
			}
		default:
			t.Error("unexpected Telegram operation: " + method)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	channel := channels.New(f.p.Store, f.p.Harness)
	f.account = domain.Connection{Name: "Sticker resources", AgentID: f.p.Agent.ID, Adapter: "telegram.bot", Enabled: true,
		Config: testkit.JSON(channels.TelegramConfig{BaseURL: server.URL})}
	token := gatewayStickerToken
	if err := channel.SaveConnection(f.p.Context, &f.account, &token); err != nil {
		t.Fatal(err)
	}
	api := New(f.p.Harness, channel)
	f.p.Hub.Gateway = api.Handle
	var err error
	f.binding, err = f.p.Hub.Bind(f.p.Context, f.p.Agent.ID, f.p.Session.ID, f.p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	f.client = testkit.ComputerAPI(t, f.p, f.binding)
	return f
}

func (f *stickerResourceGateway) request(t *testing.T, method, path string, input any, status int) (http.Header, []byte) {
	t.Helper()
	var body io.Reader
	if input != nil {
		body = bytes.NewReader(testkit.JSON(input))
	}
	request, err := http.NewRequestWithContext(f.p.Context, method, f.binding.APIURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if status != 0 && response.StatusCode != status || status == 0 && response.StatusCode < 400 {
		t.Fatalf("%s %s: status=%d body=%s", method, path, response.StatusCode, data)
	}
	return response.Header, data
}

func (f *stickerResourceGateway) resourcePath(source, file string) string {
	key, value := "set_name", "AstralGatewayStickers"
	if source == "emoji" {
		key, value = "custom_emoji_id", gatewayCustomEmojiID
	}
	query := url.Values{key: {value}, "file_id": {file}}
	return "/v1/accounts/" + f.account.ID + "/telegram/stickers/content?" + query.Encode()
}

func (f *stickerResourceGateway) assertNoReceptionSideEffects(t *testing.T, files int) {
	t.Helper()
	for kind, expected := range map[string]int{"files": files, "bindings": 0, "conversations": 0, "sessions": 1} {
		records, err := f.p.Store.List(f.p.Context, kind)
		if err != nil || len(records) != expected {
			t.Fatalf("resource access created %s: count=%d err=%v", kind, len(records), err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(f.p.Store.Directory, "astral.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var observations int
	if err := db.QueryRowContext(f.p.Context, "SELECT count(*) FROM observations").Scan(&observations); err != nil || observations != 0 || f.p.Requests.Load() != 0 {
		t.Fatalf("resource access created messages or invoked the model: observations=%d err=%v", observations, err)
	}
}

func TestTelegramStickerResourceQueriesAndStreamsHaveNoReceptionSideEffects(t *testing.T) {
	f := newStickerResourceGateway(t, 1)
	base := "/v1/accounts/" + f.account.ID
	_, catalog := f.request(t, "GET", base+"/operations", nil, http.StatusOK)
	var operations []channels.ChatOperation
	if err := json.Unmarshal(catalog, &operations); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"getStickerSet", "getCustomEmojiStickers"} {
		found := false
		for _, operation := range operations {
			if operation.Name == name {
				found = operation.AccountScoped
			}
		}
		if !found {
			t.Fatalf("%s is not discoverable as an account resource operation", name)
		}
	}
	_, set := f.request(t, "POST", base+"/operations/getStickerSet", map[string]any{"parameters": map[string]any{"name": "AstralGatewayStickers"}}, http.StatusOK)
	var result struct {
		Result struct {
			Name     string `json:"name"`
			Stickers []struct {
				FileID string `json:"file_id"`
			} `json:"stickers"`
		} `json:"result"`
	}
	if err := json.Unmarshal(set, &result); err != nil || result.Result.Name != "AstralGatewayStickers" || len(result.Result.Stickers) != 1 || result.Result.Stickers[0].FileID != "set-original" {
		t.Fatalf("unbound Session could not read a complete sticker set: %s %v", set, err)
	}
	f.request(t, "POST", base+"/operations/getCustomEmojiStickers", map[string]any{"parameters": map[string]any{"custom_emoji_ids": []string{gatewayCustomEmojiID}}}, http.StatusOK)
	if f.fileCalls.Load() != 0 || f.mediaCalls.Load() != 0 {
		t.Fatal("metadata queries downloaded sticker bytes")
	}
	for _, test := range []struct {
		source, file, media string
		body                []byte
	}{
		{"set", "set-original", "application/x-tgsticker", f.original},
		{"set", "set-preview", "image/jpeg", f.thumbnail},
		{"emoji", "emoji-original", "application/x-tgsticker", f.original},
	} {
		header, body := f.request(t, "GET", f.resourcePath(test.source, test.file), nil, http.StatusOK)
		disposition, parameters, err := mime.ParseMediaType(header.Get("Content-Disposition"))
		if !bytes.Equal(body, test.body) || header.Get("Content-Type") != test.media || header.Get("Content-Length") != strconv.Itoa(len(test.body)) || !strings.Contains(header.Get("Cache-Control"), "no-store") || err != nil || disposition != "attachment" || parameters["filename"] == "" {
			t.Fatalf("resource stream lost original bytes or safe headers: header=%v bytes=%d err=%v", header, len(body), err)
		}
	}
	f.assertNoReceptionSideEffects(t, 0)
	if err := filepath.WalkDir(f.p.Daemons[0].Root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && entry.Name() != "identity.json" {
			t.Errorf("resource read automatically saved a Computer file: %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramStickerResourceDownloadRetainsCallingComputerAfterSwitch(t *testing.T) {
	f := newStickerResourceGateway(t, 2)
	if err := f.p.Harness.SelectComputer(f.p.Context, f.p.Session.ID, f.p.Daemons[1].Credentials.ComputerID, ""); err != nil {
		t.Fatal(err)
	}
	_, data := f.request(t, "POST", "/v1/accounts/"+f.account.ID+"/telegram/stickers/download", map[string]any{"set_name": "AstralGatewayStickers", "file_id": "set-original", "path": "chosen-sticker.tgs"}, http.StatusOK)
	var saved ReferenceResponse
	if err := json.Unmarshal(data, &saved); err != nil || saved.File.ComputerID != f.binding.ComputerID || saved.URI == "" || filepath.Clean(saved.File.Path) != filepath.Join(f.binding.CWD, "chosen-sticker.tgs") {
		t.Fatalf("download moved to the newly selected Computer: %s %v", data, err)
	}
	_, _, stream, err := f.p.Harness.Files.Read(f.p.Context, f.p.Agent.ID, f.p.Session.ID, saved.URI, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(stream)
	closeErr := stream.Close()
	if err != nil || closeErr != nil || !bytes.Equal(body, f.original) {
		t.Fatalf("saved Computer file changed: bytes=%d read=%v close=%v", len(body), err, closeErr)
	}
	other, err := f.p.Hub.Bind(f.p.Context, f.p.Agent.ID, f.p.Session.ID, f.p.Daemons[1].Credentials.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	fs, err := (&computers.Backend{Hub: f.p.Hub}).FileSystem(f.p.Context, core.Invocation{SessionID: other.SessionID, Binding: computers.BindingJSON(other)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(f.p.Context, "chosen-sticker.tgs"); err == nil {
		t.Fatal("explicit download also wrote to the newly selected Computer")
	}
	f.assertNoReceptionSideEffects(t, 1)
}

func TestTelegramStickerResourceErrorsDoNotExposeCredentialsOrFetchArbitraryFiles(t *testing.T) {
	f := newStickerResourceGateway(t, 1)
	base := "/v1/accounts/" + f.account.ID
	for _, test := range []struct {
		query  string
		status int
	}{
		{"file_id=set-original", http.StatusBadRequest},
		{"set_name=AstralGatewayStickers&custom_emoji_id=" + gatewayCustomEmojiID + "&file_id=set-original", http.StatusBadRequest},
		{"set_name=AstralGatewayStickers&file_id=another-chat-private-document", http.StatusForbidden},
	} {
		_, body := f.request(t, "GET", base+"/telegram/stickers/content?"+test.query, nil, test.status)
		if !json.Valid(body) || bytes.Contains(body, []byte(gatewayStickerToken)) {
			t.Fatal("resource error exposed credentials or returned an invalid error document")
		}
	}
	f.request(t, "POST", base+"/telegram/stickers/download", map[string]any{"set_name": "AstralGatewayStickers", "file_id": "set-original"}, http.StatusBadRequest)
	f.request(t, "POST", base+"/operations/sendSticker", map[string]any{"parameters": map[string]any{"sticker": "set-original"}}, http.StatusBadRequest)
	_, body := f.request(t, "GET", base+"/telegram/stickers/content?set_name=broken&file_id=set-original", nil, 0)
	if !json.Valid(body) || bytes.Contains(body, []byte(gatewayStickerToken)) {
		t.Fatal("upstream resource failure exposed its Bot credential")
	}
	foreign := f.account
	foreign.Meta, foreign.AgentID = domain.Meta{ID: "foreign-sticker-account"}, "another-bot"
	if err := f.p.Store.Save(f.p.Context, "connections", &foreign.Meta, &foreign); err != nil {
		t.Fatal(err)
	}
	f.request(t, "GET", strings.Replace(f.resourcePath("set", "set-original"), f.account.ID, foreign.ID, 1), nil, http.StatusForbidden)
	if f.fileCalls.Load() != 0 || f.mediaCalls.Load() != 0 {
		t.Fatal("invalid or unselected resource fetched bytes")
	}
	f.assertNoReceptionSideEffects(t, 0)
}

func TestTelegramStickerResourceDownloadPathsAreDiscoverableAndAuthenticated(t *testing.T) {
	f := newStickerResourceGateway(t, 1)
	_, body := f.request(t, "GET", "/v1/capabilities/files", nil, http.StatusOK)
	var document CapabilityDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	paths := document.OpenAPI["paths"].(map[string]any)
	for _, path := range []string{"/v1/accounts/{id}/telegram/stickers/content", "/v1/accounts/{id}/telegram/stickers/download"} {
		if paths[path] == nil {
			t.Fatalf("sticker resource API has no discoverable contract: %s", path)
		}
	}
	content, download := false, false
	for _, example := range document.Examples {
		parsed, err := url.Parse(example.Path)
		if err != nil || parsed.IsAbs() || !strings.HasPrefix(example.Path, "/") {
			t.Fatalf("file API example is not relative to local_api.base_url: %+v", example)
		}
		content = content || strings.Contains(parsed.Path, "/telegram/stickers/content") && example.Method == http.MethodGet
		download = download || strings.Contains(parsed.Path, "/telegram/stickers/download") && example.Method == http.MethodPost && example.Body != nil
	}
	if !content || !download {
		t.Fatal("file discovery omitted an explicit sticker read/download example")
	}
	get := paths["/v1/accounts/{id}/telegram/stickers/content"].(map[string]any)["get"].(map[string]any)
	requiredFile := false
	for _, raw := range get["parameters"].([]any) {
		parameter := raw.(map[string]any)
		requiredFile = requiredFile || parameter["name"] == "file_id" && parameter["in"] == "query" && parameter["required"] == true
	}
	if !requiredFile {
		t.Fatal("content contract does not require its selected file_id")
	}
	_, rawContract := f.request(t, "GET", "/v1/openapi.json", nil, http.StatusOK)
	var full map[string]any
	if err := json.Unmarshal(rawContract, &full); err != nil {
		t.Fatal(err)
	}
	requestSchema := func(path string) map[string]any {
		t.Helper()
		operation := full["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)
		schema := operation["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		name := strings.TrimPrefix(schema["$ref"].(string), "#/components/schemas/")
		return full["components"].(map[string]any)["schemas"].(map[string]any)[name].(map[string]any)
	}
	required := func(schema map[string]any, name string) bool {
		for _, raw := range schema["required"].([]any) {
			if raw == name {
				return true
			}
		}
		return false
	}
	downloadSchema := requestSchema("/v1/accounts/{id}/telegram/stickers/download")
	if !required(downloadSchema, "file_id") || !required(downloadSchema, "path") {
		t.Fatal("download contract omitted mandatory file_id or explicit path")
	}
	if required(requestSchema("/v1/accounts/{id}/operations/{operation}"), "conversation_id") {
		t.Fatal("named operation contract still requires a chat for resource reads")
	}
	request, err := http.NewRequestWithContext(f.p.Context, "GET", f.binding.APIURL+f.resourcePath("set", "set-original"), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || f.fileCalls.Load() != 0 {
		t.Fatal("anonymous sticker content request reached the platform")
	}
}

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/testkit"
)

func TestNapCatEssenceGatewayReadsUnarchivedImagesWithSafeStableLinks(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	var nativeID, removed atomic.Bool
	var images atomic.Int64
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_essence_msg_list":
			var parameters map[string]string
			_ = json.NewDecoder(r.Body).Decode(&parameters)
			if len(parameters) != 1 || parameters["group_id"] != "456" {
				t.Errorf("source lookup escaped group: %+v", parameters)
			}
			if removed.Load() {
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":[]}`)
				return
			}
			id := 100
			if nativeID.Load() {
				id = -200
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "retcode": 0, "data": []any{map[string]any{
				"message_id": id, "msg_seq": 10, "msg_random": 20, "content": []any{
					map[string]any{"type": "text", "data": map[string]any{"text": "caption"}},
					map[string]any{"type": "image", "data": map[string]any{"file": "", "url": server.URL + "/image?signature=private"}},
				},
			}}})
		case "/image":
			if r.URL.Query().Get("signature") != "private" {
				t.Error("media did not use the freshly verified source")
			}
			images.Add(1)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(picture.Bytes())
		default:
			t.Errorf("unexpected provider API or archive-dependent fallback: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	account := domain.Connection{Meta: domain.Meta{ID: "essence-account"}, AgentID: p.Agent.ID, Adapter: "qq.napcat", Platform: "qq", AccountID: "999", Enabled: true, AccessMode: "all",
		Config: testkit.JSON(channels.NapCatConfig{BaseURL: server.URL, WebSocketURL: strings.Replace(server.URL, "http:", "ws:", 1) + "/ws"})}
	chat := domain.Conversation{Meta: domain.Meta{ID: "essence-chat"}, ConnectionID: account.ID, ExternalID: "group:456", Kind: "group"}
	if err := p.Store.Save(p.Context, "connections", &account.Meta, &account); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
		t.Fatal(err)
	}
	// Register only this resource family, independently of concurrent route wiring.
	g := &Service{Harness: p.Harness, Channels: channels.New(p.Store, p.Harness), Contract: httpcontract.New(), mux: http.NewServeMux()}
	g.napcatEssenceRoutes()
	p.Hub.Gateway = g.Handle
	b, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := testkit.ComputerAPI(t, p, b)
	call := func(method, path string, body any, status int) (http.Header, []byte) {
		t.Helper()
		var payload []byte
		if body != nil {
			payload = testkit.JSON(body)
		}
		request, err := http.NewRequestWithContext(p.Context, method, b.APIURL+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("%s %s: status=%d body=%s err=%v", method, path, response.StatusCode, data, err)
		}
		return response.Header, data
	}
	const root = "/v1/conversations/essence-chat/essence/100/parts"
	_, data := call("GET", root, nil, 200)
	for _, secret := range []string{server.URL, "signature", "external_id", "\"raw\""} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("safe essence projection exposed provider data %q: %s", secret, data)
		}
	}
	var parts MessagePartsResponse
	if err := json.Unmarshal(data, &parts); err != nil || len(parts.Parts) != 2 || parts.Parts[0].Text != "caption" || parts.Parts[1].Attachment == nil {
		t.Fatalf("ordered essence content lost: %+v %v", parts, err)
	}
	image := parts.Parts[1]
	if !strings.HasPrefix(image.ContentURL, root+"/p1/content?") || !strings.Contains(image.ContentURL, "msg_seq=10") || !strings.Contains(image.ContentURL, "msg_random=20") || !strings.Contains(image.ContentURL, "media_digest=") || images.Load() != 0 {
		t.Fatalf("missing stable scoped resource links or eager download: %+v", image)
	}
	nativeID.Store(true)
	call("GET", image.ReadURL, nil, 200)
	headers, content := call("GET", image.ContentURL, nil, 200)
	if !bytes.Equal(content, picture.Bytes()) || headers.Get("Content-Type") != "image/png" || !strings.Contains(headers.Get("Content-Disposition"), ".png") || headers.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("image response is not original pixels: %s %v", content, headers)
	}
	// Preserve an explicit caller path even if its extension is misleading;
	// the returned and persisted FileRef must retain the actual media type.
	destination := filepath.Join(p.Directory, "essence-photo.jpg")
	_, data = call("POST", image.DownloadURL, MessagePartDownloadRequest{Path: destination}, 200)
	var saved ReferenceResponse
	if err := json.Unmarshal(data, &saved); err != nil || saved.File.ComputerID != b.ComputerID || saved.File.Path != destination || saved.File.MediaType != "image/png" || !strings.HasPrefix(saved.URI, "astral-file://") {
		t.Fatalf("explicit download lost source Computer/reference: %s %v", data, err)
	}
	if content, err := os.ReadFile(destination); err != nil || !bytes.Equal(content, picture.Bytes()) {
		t.Fatalf("explicit download saved wrong content: %s %v", content, err)
	}
	var persisted domain.FileRef
	if err := p.Store.Get(p.Context, "files", saved.File.ID, &persisted); err != nil || persisted.MediaType != "image/png" {
		t.Fatalf("actual PNG type was not persisted: %+v %v", persisted, err)
	}
	call("GET", root+"/p1/content?msg_seq=10&msg_random=20", nil, 400)
	call("GET", root+"?msg_seq=10", nil, 400)
	call("GET", root+"?msg_seq=10&msg_seq=10&msg_random=20", nil, 400)
	call("GET", root+"/p1/content?url="+server.URL, nil, 400)
	call("POST", image.DownloadURL, map[string]any{"path": destination, "url": server.URL}, 400)
	removed.Store(true)
	call("GET", image.ContentURL, nil, 404)
	account.AccessMode = "specified"
	if err := p.Store.Save(p.Context, "connections", &account.Meta, &account); err != nil {
		t.Fatal(err)
	}
	call("GET", image.ContentURL, nil, 403)
	if images.Load() != 2 || p.Requests.Load() != 0 {
		t.Fatal("rejected/list requests downloaded extra media or ran an Agent")
	}
	if _, err := p.Store.Message(p.Context, chat.ID, "100"); err == nil {
		t.Fatal("resource API silently imported an archive message")
	}
}

func TestNapCatEssenceContractsUseExistingSafePartAndDownloadSchemas(t *testing.T) {
	g := &Service{Contract: httpcontract.New(), mux: http.NewServeMux()}
	g.napcatEssenceRoutes()
	const root = "/v1/conversations/{id}/essence/{message_id}/parts"
	for path, method := range map[string]string{root: "get", root + "/{ref}": "get", root + "/{ref}/content": "get", root + "/{ref}/download": "post"} {
		if g.Contract.Paths[path] == nil || g.Contract.Paths[path].(map[string]any)[method] == nil {
			t.Fatalf("missing essence resource contract: %s %s", method, path)
		}
	}
	schema, err := json.Marshal(g.Contract.Schemas["astral_internal_gateway_MessagePartView"])
	if err != nil || len(schema) < 10 || strings.Contains(string(schema), "external_id") || strings.Contains(string(schema), `"raw"`) {
		t.Fatalf("unsafe or missing resource schema: %s %v", schema, err)
	}
}

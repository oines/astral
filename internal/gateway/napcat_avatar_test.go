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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

type napcatAvatarGatewayTransport func(*http.Request) (*http.Response, error)

func (transport napcatAvatarGatewayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func TestNapCatAvatarGatewayReadsScopedImages(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	var images, membership atomic.Int64
	s := channels.New(p.Store, p.Harness)
	s.Client = &http.Client{Transport: napcatAvatarGatewayTransport(func(r *http.Request) (*http.Response, error) {
		header := http.Header{}
		var body []byte
		switch r.URL.Hostname() {
		case "127.0.0.1":
			if r.Method != "POST" || r.URL.Path != "/get_group_member_info" {
				t.Errorf("unexpected platform operation: %s %s", r.Method, r.URL)
			}
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["group_id"] != "456" || input["no_cache"] != true {
				t.Errorf("member lookup lost current group: %+v %v", input, err)
			}
			membership.Add(1)
			// A different returned identity does not prove the requested user
			// is a member, even when NapCat reports a successful envelope.
			body = []byte(`{"status":"ok","retcode":0,"data":{"group_id":456,"user_id":123}}`)
			header.Set("Content-Type", "application/json")
		case "q1.qlogo.cn":
			if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Path != "/g" || r.URL.Query().Get("b") != "qq" || r.URL.Query().Get("nk") != "123" || r.URL.Query().Get("s") != "100" {
				t.Errorf("individual avatar escaped the verified user: %s %s", r.Method, r.URL)
			}
			images.Add(1)
			body = picture.Bytes()
			header.Set("Content-Type", "image/png")
		case "p.qlogo.cn":
			if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Path != "/gh/456/456/0/" || r.URL.RawQuery != "" {
				t.Errorf("group avatar escaped the selected group: %s %s", r.Method, r.URL)
			}
			images.Add(1)
			body = picture.Bytes()
			header.Set("Content-Type", "image/png")
		default:
			t.Errorf("avatar reached an arbitrary host: %s", r.URL)
			return nil, fmt.Errorf("unexpected avatar host")
		}
		if r.URL.Hostname() != "127.0.0.1" && r.Header.Get("Authorization") != "" {
			t.Error("NapCat credentials leaked to avatar service")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	account := domain.Connection{Meta: domain.Meta{ID: "avatar-account"}, AgentID: p.Agent.ID, Adapter: "qq.napcat", Platform: "qq", AccountID: "999", Enabled: true, AccessMode: "all",
		Config: testkit.JSON(channels.NapCatConfig{BaseURL: "http://127.0.0.1:10001", WebSocketURL: "ws://127.0.0.1:10001/ws"})}
	group := domain.Conversation{Meta: domain.Meta{ID: "avatar-group"}, ConnectionID: account.ID, ExternalID: "group:456", Kind: "group"}
	private := domain.Conversation{Meta: domain.Meta{ID: "avatar-private"}, ConnectionID: account.ID, ExternalID: "private:123", Kind: "private"}
	if err := p.Store.Save(p.Context, "connections", &account.Meta, &account); err != nil {
		t.Fatal(err)
	}
	for _, chat := range []*domain.Conversation{&group, &private} {
		if err := p.Store.Save(p.Context, "conversations", &chat.Meta, chat); err != nil {
			t.Fatal(err)
		}
	}
	g := New(p.Harness, s)
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
	for _, root := range []string{
		"/v1/conversations/avatar-group/avatar",
		"/v1/conversations/avatar-private/avatar",
		"/v1/conversations/avatar-group/members/123/avatar",
		"/v1/conversations/avatar-private/members/123/avatar",
	} {
		headers, data := call("GET", root+"/content", nil, 200)
		if !bytes.Equal(data, picture.Bytes()) || headers.Get("Content-Type") != "image/png" || headers.Get("Content-Length") != strconv.Itoa(picture.Len()) || !strings.Contains(headers.Get("Content-Disposition"), ".png") || headers.Get("Cache-Control") != "private, no-store" || headers.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("avatar stream lost verified bytes or media metadata: %v body=%x", headers, data)
		}
	}
	if images.Load() != 4 || membership.Load() != 1 {
		t.Fatalf("missing image reads or fresh membership checks: images=%d members=%d", images.Load(), membership.Load())
	}
	const groupRoot = "/v1/conversations/avatar-group/avatar"
	call("GET", groupRoot+"/content?url=https://example.com/avatar", nil, 400)
	call("GET", "/v1/conversations/avatar-private/members/888/avatar/content", nil, 403)
	call("GET", "/v1/conversations/avatar-group/members/888/avatar/content", nil, 403)
	account.AccessMode = "specified"
	if err := p.Store.Save(p.Context, "connections", &account.Meta, &account); err != nil {
		t.Fatal(err)
	}
	call("GET", groupRoot+"/content", nil, 403)
	if images.Load() != 4 || p.Requests.Load() != 0 {
		t.Fatal("rejected requests downloaded an avatar or activated the model")
	}
	for _, chat := range []domain.Conversation{group, private} {
		items, err := p.Store.SearchObservations(p.Context, chat.ID, 0, 10, "")
		if err != nil || len(items) != 0 {
			t.Fatalf("avatar resource access changed IM history: %+v %v", items, err)
		}
	}
}

func TestNapCatAvatarGatewayContractsAreDiscoverable(t *testing.T) {
	g := New(nil, nil)
	recorder := httptest.NewRecorder()
	g.mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/v1/capabilities/files", nil))
	var discovery CapabilityDocument
	if err := json.Unmarshal(recorder.Body.Bytes(), &discovery); err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("files capability discovery failed: %s %v", recorder.Body.Bytes(), err)
	}
	paths := discovery.OpenAPI["paths"].(map[string]any)
	for _, root := range []string{"/v1/conversations/{id}/avatar", "/v1/conversations/{id}/members/{user_id}/avatar"} {
		path := root + "/content"
		if g.Contract.Paths[path] == nil || paths[path] == nil {
			t.Fatalf("registered avatar endpoint missing from discovery: %s", path)
		}
		operation := paths[path].(map[string]any)["get"].(map[string]any)
		security, _ := json.Marshal(operation["security"])
		if string(security) != `[{"command":[]}]` {
			t.Fatalf("avatar resource omitted command authorization: %s %s", path, security)
		}
		response := operation["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
		if response["application/octet-stream"] == nil {
			t.Fatalf("avatar read does not describe binary content: %s", path)
		}
	}
}

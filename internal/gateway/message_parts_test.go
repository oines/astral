package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestMessagePartGatewayKeepsParentScopeAndHidesSourceHandles(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	var mediaCalls atomic.Int64
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/original" || r.URL.Query().Get("signature") != "secret-media-signature" {
			t.Errorf("unexpected provider resource request: %s", r.URL)
			http.NotFound(w, r)
			return
		}
		mediaCalls.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, "original-bytes")
	}))
	defer source.Close()
	account := domain.Connection{Meta: domain.Meta{ID: "part-account"}, AgentID: p.Agent.ID, Adapter: "qq.napcat", Platform: "qq", Enabled: true, AccessMode: "all", AccountID: "999",
		Config: testkit.JSON(channels.NapCatConfig{BaseURL: source.URL, WebSocketURL: strings.Replace(source.URL, "http:", "ws:", 1) + "/ws"})}
	if err := p.Store.Save(p.Context, "connections", &account.Meta, &account); err != nil {
		t.Fatal(err)
	}
	chat := domain.Conversation{Meta: domain.Meta{ID: "part-chat"}, ConnectionID: account.ID, ExternalID: "group:456", Kind: "group", Title: "Allowed"}
	if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
		t.Fatal(err)
	}
	attachment := domain.Attachment{Ref: "p1.m0.p0", ExternalID: string(testkit.JSON(map[string]string{"file": "secret-platform-handle", "url": source.URL + "/original?signature=secret-media-signature"})), Kind: "image", Name: "quoted.png", MediaType: "image/png", Size: 14}
	quoted := domain.ForwardedMessage{Sender: domain.Sender{ID: "7", Name: "Quoted author"}, Parts: []domain.MessagePart{{Ref: "p1.m0.p0", Type: "attachment", Attachment: &attachment}}}
	inline := testkit.JSON([]any{map[string]any{"sender": map[string]any{"user_id": "7", "nickname": "Quoted author"}, "group_id": "other-group-is-quoted", "message": []any{
		map[string]any{"type": "image", "data": map[string]any{"file": "secret-platform-handle", "url": source.URL + "/original?signature=secret-media-signature", "name": "quoted.png", "file_size": 14}},
	}}})
	cardRaw := testkit.JSON(map[string]any{"app": "com.example.card", "private_raw": "explicit-card-payload"})
	event := domain.Observation{SchemaVersion: 1, ID: "parts-source-event", ConnectionID: account.ID, ConversationID: chat.ID, MessageID: "42", Kind: "message.created", ExternalEventID: "42", SentAt: time.Now(), ReceivedAt: time.Now(), Parts: []domain.MessagePart{
		{Ref: "p0", Type: "card", Card: &domain.MessageCard{Kind: "json", Source: "QQ card", Title: "Shared article", URL: "https://example.com/article", Raw: cardRaw}},
		{Ref: "p1", Type: "forward", Forward: &domain.MessageForward{ExternalID: "secret-forward-handle", Raw: inline, Status: "inline", Total: 1, Messages: []domain.ForwardedMessage{quoted}}},
	}}
	event.ReplyID, event.ReplyParts = "41", event.Parts
	archived := testkit.JSON(event)
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	s := channels.New(p.Store, p.Harness)
	g := New(p.Harness, s)
	p.Hub.Gateway = g.Handle
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := testkit.ComputerAPI(t, p, binding)
	call := func(method, path string, body any, status int) (http.Header, []byte) {
		t.Helper()
		var data []byte
		if body != nil {
			data = testkit.JSON(body)
		}
		request, err := http.NewRequestWithContext(p.Context, method, binding.APIURL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err = io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("%s %s: status=%d, body=%s, err=%v", method, path, response.StatusCode, data, err)
		}
		return response.Header, data
	}
	const base = "/v1/conversations/part-chat/messages/42/parts"
	assertSafeTree := func(data []byte) {
		t.Helper()
		for _, forbidden := range []string{"external_id", `"raw":`, "secret-platform-handle", "secret-forward-handle", "secret-media-signature", source.URL, "explicit-card-payload", "preview_url"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("normalized message tree leaked %q: %s", forbidden, data)
			}
		}
	}
	_, messageData := call("GET", "/v1/conversations/part-chat/messages/42", nil, 200)
	assertSafeTree(messageData)
	var message MessageObservationView
	if err := json.Unmarshal(messageData, &message); err != nil || len(message.Parts) != 2 || len(message.ReplyParts) != 2 || message.Parts[1].Forward.Messages[0].Parts[0].ContentURL != base+"/p1.m0.p0/content" || message.ReplyParts[1].Forward.Messages[0].Parts[0].ContentURL != "/v1/conversations/part-chat/messages/41/parts/p1.m0.p0/content" {
		t.Fatalf("message query lost safe parts or quoted source identity: %+v %v", message, err)
	}
	_, pageData := call("GET", "/v1/conversations/part-chat/messages", nil, 200)
	assertSafeTree(pageData)
	var page MessagePage
	if err := json.Unmarshal(pageData, &page); err != nil || len(page.Items) != 1 || page.Items[0].Sequence != page.NextSequence || page.Items[0].Observation.MessageID != "42" || len(page.Items[0].Observation.Parts) != 2 || len(page.Items[0].Observation.ReplyParts) != 2 || page.Items[0].Observation.ReplyParts[1].Forward.Messages[0].Parts[0].ContentURL != "/v1/conversations/part-chat/messages/41/parts/p1.m0.p0/content" {
		t.Fatalf("message list lost pagination or safe quoted tree: %+v %v", page, err)
	}
	stored, err := p.Store.Observation(p.Context, event.ID)
	if err != nil || !bytes.Equal(testkit.JSON(stored), archived) || !bytes.Equal(testkit.JSON(event), archived) {
		t.Fatal("message projections rewrote archived or in-memory source trees", err)
	}
	_, data := call("GET", base, nil, 200)
	assertSafeTree(data)
	var roots MessagePartsResponse
	if err := json.Unmarshal(data, &roots); err != nil || len(roots.Parts) != 2 || roots.Parts[0].RawURL != base+"/p0/raw" || roots.Parts[1].Forward.Messages[0].Parts[0].ContentURL != base+"/p1.m0.p0/content" {
		t.Fatalf("root parts are not discoverable: %+v %v", roots, err)
	}
	_, raw := call("GET", roots.Parts[0].RawURL, nil, 200)
	if !bytes.Equal(bytes.TrimSpace(raw), cardRaw) {
		t.Fatalf("explicit raw read lost original card: %s", raw)
	}
	_, selected := call("GET", base+"/p1?offset=0&limit=1", nil, 200)
	if strings.Contains(string(selected), "secret-") || !strings.Contains(string(selected), "Quoted author") {
		t.Fatalf("selected part projection leaked source metadata or lost quoted sender: %s", selected)
	}
	if mediaCalls.Load() != 0 || p.Requests.Load() != 0 {
		t.Fatal("listing parts downloaded media or started an Agent")
	}
	for _, query := range []string{"?offset=-1", "?limit=0", "?limit=51", "?offset=1&offset=2", "?external_id=secret", "?limit=no"} {
		call("GET", base+"/p1"+query, nil, 400)
	}
	call("GET", base+"/p00", nil, 400)
	call("GET", "/v1/conversations/part-chat/messages/secret-forward-handle/parts/p0", nil, 404)
	header, content := call("GET", base+"/p1.m0.p0/content", nil, 200)
	if string(content) != "original-bytes" || header.Get("Cache-Control") != "private, no-store" || !strings.Contains(header.Get("Content-Disposition"), "quoted.png") {
		t.Fatalf("nested original stream lost bytes or cache restrictions: %s %v", content, header)
	}
	call("POST", base+"/p1.m0.p0/download", map[string]any{"path": ""}, 400)
	destination := filepath.Join(p.Directory, "downloaded-quote.png")
	call("POST", base+"/p1.m0.p0/download", map[string]any{"path": destination, "external_id": "arbitrary"}, 400)
	_, saved := call("POST", base+"/p1.m0.p0/download", MessagePartDownloadRequest{Path: destination}, 200)
	var reference ReferenceResponse
	if err := json.Unmarshal(saved, &reference); err != nil || reference.URI == "" || reference.File.ComputerID != binding.ComputerID {
		t.Fatalf("explicit nested download lost file reference: %s %v", saved, err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "original-bytes" {
		t.Fatalf("explicit nested download did not save original: %q %v", data, err)
	}
	account.AccessMode = "specified"
	if err := p.Store.Save(p.Context, "connections", &account.Meta, &account); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/p0/raw", "/p1", "/p1.m0.p0/content"} {
		call("GET", base+suffix, nil, 403)
	}
	call("POST", base+"/p1.m0.p0/download", MessagePartDownloadRequest{Path: destination}, 403)
	if mediaCalls.Load() != 2 || p.Requests.Load() != 0 {
		t.Fatal("invalid or revoked resource requests acquired media or invoked a model")
	}
}

func TestMessagePartContractsAreDiscoverableAndDoNotExposeWireFields(t *testing.T) {
	g := New(nil, nil)
	paths := g.Contract.Paths
	const root = "/v1/conversations/{id}/messages/{message_id}/parts"
	for path, method := range map[string]string{root: "get", root + "/{ref}": "get", root + "/{ref}/raw": "get", root + "/{ref}/content": "get", root + "/{ref}/download": "post"} {
		operation := paths[path].(map[string]any)[method].(map[string]any)
		if operation["description"] == nil {
			t.Errorf("part operation is missing discovery documentation: %s", path)
		}
	}
	if paths[root+"/{ref}/preview"] != nil {
		t.Fatal("unsolicited context-injection preview API is registered")
	}
	schema := g.Contract.Schemas["astral_internal_gateway_MessagePartView"]
	if schema == nil {
		t.Fatal("normalized part schema is not registered")
	}
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"external_id", `"raw"`, "file_uri", "preview_url"} {
		if strings.Contains(string(data), field) {
			t.Errorf("part schema exposes wire field %s: %s", field, data)
		}
	}
	observation := g.Contract.Schemas["astral_internal_gateway_MessageObservationView"].(map[string]any)
	properties := observation["properties"].(map[string]any)
	for _, field := range []string{"parts", "reply_parts"} {
		data, err := json.Marshal(properties[field])
		if err != nil || !strings.Contains(string(data), "astral_internal_gateway_MessagePartView") || strings.Contains(string(data), "domain_MessagePart") {
			t.Errorf("message schema exposes the archived %s tree: %s %v", field, data, err)
		}
	}
}

func TestMessageObservationViewRetainsExistingAttachmentHandles(t *testing.T) {
	attachment := domain.Attachment{Ref: "original:0", ExternalID: "platform-file-id", Kind: "file", Name: "original.bin"}
	observation := domain.Observation{ConversationID: "chat", MessageID: "42", Attachments: []domain.Attachment{attachment}, ReplyAttachments: []domain.Attachment{attachment}, Parts: []domain.MessagePart{{Ref: "p0", Type: "text", Text: "hello"}}}
	view := MessageObservation(observation)
	if len(view.Attachments) != 1 || view.Attachments[0].ExternalID != attachment.ExternalID || len(view.ReplyAttachments) != 1 || view.ReplyAttachments[0].ExternalID != attachment.ExternalID || view.Observation.Parts != nil || view.Observation.ReplyParts != nil || len(observation.Parts) != 1 {
		t.Fatal("safe tree projection changed legacy attachment capabilities or retained archived trees")
	}
}

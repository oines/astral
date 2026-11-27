package acp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/testkit"
)

func tinyPromptPNG(t *testing.T) string {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}

func TestACPInlineImageUsesComputerAttachmentAndFrozenModelContent(t *testing.T) {
	encoded := tinyPromptPNG(t)
	modelImages := make(chan string, 1)
	p := testkit.New(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		if request["tools"] == nil {
			return testkit.Text("title")
		}
		select {
		case modelImages <- string(testkit.JSON(request["messages"])):
		default:
		}
		return testkit.Text("I can see the image")
	})
	service := New(p.Harness)
	server := httptest.NewServer(service)
	defer server.Close()
	token, err := service.CreateToken(p.Context, p.Agent.ID, "Editor")
	if err != nil {
		t.Fatal(err)
	}
	client, _, err := dial(t, server.URL, token.Token)
	if err != nil {
		t.Fatal(err)
	}
	client.initialize(t)
	sessionID := p.Session.ID
	client.send(t, 2, "session/load", map[string]any{"sessionId": sessionID, "cwd": p.Session.CWD, "mcpServers": []any{}})
	if reply, _ := client.response(t, 2); reply.Error != nil {
		t.Fatalf("load fixture session: %+v", *reply.Error)
	}

	valid := contentBlock{Type: "image", Data: encoded, MimeType: "image/png"}
	bad := [][]contentBlock{
		{{Type: "image", Data: "!invalid!", MimeType: "image/png"}},
		{{Type: "image", Data: encoded, MimeType: "image/jpeg"}},
		{{Type: "image", URI: "https://example.invalid/image.png", MimeType: "image/png"}},
		{valid, valid, valid, valid, valid},
		{valid, {Type: "image", Data: base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n")), MimeType: "image/png"}},
	}
	for i, prompt := range bad {
		id := i + 3
		client.send(t, id, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": prompt})
		if reply, _ := client.response(t, id); reply.Error == nil || reply.Error.Code != -32602 {
			t.Fatalf("bad image case %d returned %+v", i, reply)
		}
	}
	files, err := p.Store.List(p.Context, "files")
	if err != nil || len(files) != 0 {
		t.Fatalf("an invalid prompt persisted attachments: count=%d err=%v", len(files), err)
	}

	id := len(bad) + 3
	client.send(t, id, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": []contentBlock{valid}})
	if reply, _ := client.response(t, id); reply.Error != nil || reply.Result.StopReason != "end_turn" {
		t.Fatalf("image-only prompt: %+v", reply)
	}
	select {
	case request := <-modelImages:
		if !strings.Contains(request, "data:image/png;base64,"+encoded) || !strings.Contains(request, "astral-file://"+p.Agent.DefaultComputerID+"/") {
			t.Fatal("model request did not contain both the image and its Computer attachment reference")
		}
	case <-client.ctx.Done():
		t.Fatal(client.ctx.Err())
	}
	session, err := p.Harness.Core(p.Context, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.History().Scan(p.Context, core.Query{SessionID: sessionID, HistoryOnly: true, Limit: 64})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range page.Entries {
		if entry.Message == nil || entry.Message.Role != core.RoleUser {
			continue
		}
		for _, part := range entry.Message.Content {
			if part.Type != "image" {
				continue
			}
			var stored string
			if err := json.Unmarshal(part.Data, &stored); err != nil || stored != encoded || !strings.HasPrefix(part.URL, "astral-file://") {
				t.Fatal("the stored message did not retain the frozen image and its attachment reference")
			}
			return
		}
	}
	t.Fatal("the image was not found in retained session history")
}

func TestACPInlineImageRequiresSessionComputerAndAllowsTextRetry(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("text answer") })
	service := New(p.Harness)
	server := httptest.NewServer(service)
	defer server.Close()
	token, err := service.CreateToken(p.Context, p.Agent.ID, "Editor")
	if err != nil {
		t.Fatal(err)
	}
	client, _, err := dial(t, server.URL, token.Token)
	if err != nil {
		t.Fatal(err)
	}
	client.initialize(t)
	id := client.create(t)
	client.send(t, 3, "session/prompt", map[string]any{"sessionId": id, "prompt": []contentBlock{{Type: "image", Data: tinyPromptPNG(t), MimeType: "image/png"}}})
	if reply, _ := client.response(t, 3); reply.Error == nil || reply.Error.Code != -32602 || !strings.Contains(reply.Error.Message, "Select a Computer") {
		t.Fatalf("image without a Computer: %+v", reply)
	}
	client.send(t, 4, "session/prompt", map[string]any{"sessionId": id, "prompt": []contentBlock{{Type: "text", Text: "text only"}}})
	if reply, _ := client.response(t, 4); reply.Error != nil || reply.Result.StopReason != "end_turn" {
		t.Fatalf("text retry: %+v", reply)
	}
}

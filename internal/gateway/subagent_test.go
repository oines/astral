package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestSubagentComputerAPIKeepsReadsAndRejectsExternalSends(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	c := channels.New(p.Store, p.Harness)
	adapter := &sendAdapter{}
	c.Registry["fixture"] = func(domain.Connection, string, *http.Client) (channels.Adapter, error) { return adapter, nil }
	account := domain.Connection{Name: "fixture", AgentID: p.Agent.ID, Adapter: "fixture", AccessMode: "all", Enabled: true}
	token := "fixture-token"
	if err := c.SaveConnection(p.Context, &account, &token); err != nil {
		t.Fatal(err)
	}
	chat := domain.Conversation{Meta: domain.Meta{ID: domain.ID("conversation")}, ConnectionID: account.ID, ExternalID: "42", Kind: "private"}
	if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
		t.Fatal(err)
	}
	child, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "Internal API caller")
	if err != nil {
		t.Fatal(err)
	}
	child.SubagentParentID = p.Session.ID
	if err := p.Store.Save(p.Context, "sessions", &child.Meta, &child); err != nil {
		t.Fatal(err)
	}
	api := New(p.Harness, c)
	p.Hub.Gateway = api.Handle
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, child.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := testkit.ComputerAPI(t, p, binding)
	for _, path := range []string{"/v1/context", "/v1/conversations", "/v1/capabilities"} {
		request, err := http.NewRequestWithContext(p.Context, http.MethodGet, binding.APIURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || !json.Valid(raw) {
			t.Fatalf("child API read %s: status=%d body=%s error=%v", path, response.StatusCode, raw, readErr)
		}
	}
	for _, path := range []string{"/v1/im/messages", "/v1/im/files"} {
		body := map[string]any{"conversation_id": chat.ID, "text": "must not reach IM"}
		if path == "/v1/im/files" {
			body["path"] = "/tmp/child-report.txt"
		}
		request, err := http.NewRequestWithContext(p.Context, http.MethodPost, binding.APIURL+path, strings.NewReader(string(testkit.JSON(body))))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "internal-send")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusForbidden || !strings.Contains(string(raw), "direct parent") {
			t.Fatalf("child API send %s: status=%d body=%s error=%v", path, response.StatusCode, raw, readErr)
		}
	}
	rows, err := p.Store.List(p.Context, "outbox")
	if err != nil || len(rows) != 0 || adapter.calls.Load() != 0 {
		t.Fatalf("rejected gateway sends left side effects: intents=%d sends=%d error=%v", len(rows), adapter.calls.Load(), err)
	}
}

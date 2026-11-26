package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/mcpclient"
	"github.com/oines/astral/internal/testkit"
)

func TestComputerGatewayMCPUsesCurrentRootAndRevokesCalls(t *testing.T) {
	const secret = "Bearer gateway-mcp-fixture-credential"
	const toolName = "mcp__fixture__echo"
	const callPath = "/v1/mcp/tools/" + toolName + "/call"
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != secret {
			t.Error("remote MCP did not receive its mounted authorization")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": struct{}{}}}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			result = map[string]any{"tools": []mcpclient.Tool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
		case "tools/call":
			calls.Add(1)
			if request.Params.Name != "echo" || string(request.Params.Arguments) != `{"value":"command data"}` {
				t.Error("Computer API did not route the tool name and arguments")
			}
			result = mcpclient.Result{Content: []mcpclient.Content{{Type: "text", Text: "echoed"}}, StructuredContent: json.RawMessage(`{"verified":true}`)}
		default:
			t.Errorf("unexpected MCP method %s", request.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(remote.Close)
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	gateway := New(p.Harness, channels.New(p.Store, p.Harness))
	p.Hub.Gateway = gateway.Handle
	otherSession, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "Independent root")
	if err != nil {
		t.Fatal(err)
	}
	forSession := func(id string) (domain.ExecutionBinding, *http.Client) {
		t.Helper()
		binding, err := p.Hub.Bind(p.Context, p.Agent.ID, id, p.Agent.DefaultComputerID, "")
		if err != nil {
			t.Fatal(err)
		}
		return binding, testkit.ComputerAPI(t, p, binding)
	}
	root, rootClient := forSession(p.Session.ID)
	other, otherClient := forSession(otherSession.ID)
	if root.APIURL != other.APIURL {
		t.Fatal("test must use two credentials on the same Computer's Gateway")
	}
	request := func(client *http.Client, binding domain.ExecutionBinding, method, path, body string, wantStatus int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(p.Context, method, binding.APIURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != wantStatus {
			t.Fatalf("%s %s returned %d, expected %d: %s, %v", method, path, res.StatusCode, wantStatus, raw, err)
		}
		if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(remote.URL)) {
			t.Fatal("Computer response leaked MCP connection information")
		}
		return raw
	}
	if err := p.Harness.MountACPTools(p.Context, p.Session.ID, "mcp-gateway-owner", []mcpclient.ServerSpec{{Type: "http", Name: "fixture", URL: remote.URL, Headers: []mcpclient.Header{{Name: "Authorization", Value: secret}}}}); err != nil {
		t.Fatal(err)
	}
	raw := request(rootClient, root, http.MethodGet, "/v1/capabilities/mcp", "", http.StatusOK)
	if !bytes.Contains(raw, []byte("/v1/mcp/tools/{name}/call")) {
		t.Fatal("MCP discovery did not expose the tool call route")
	}
	raw = request(rootClient, root, http.MethodGet, "/v1/mcp/tools", "", http.StatusOK)
	var tools []core.ToolDefinition
	if err := json.Unmarshal(raw, &tools); err != nil || len(tools) != 1 || tools[0].Name != toolName || !bytes.Equal(tools[0].Parameters, []byte(`{"type":"object"}`)) {
		t.Fatalf("Computer exposed wrong MCP declarations: %s %v", raw, err)
	}
	raw = request(otherClient, other, http.MethodGet, "/v1/mcp/tools", "", http.StatusOK)
	if string(bytes.TrimSpace(raw)) != `[]` {
		t.Fatalf("another root saw mounted MCP tools: %s", raw)
	}
	const body = `{"arguments":{"value":"command data"}}`
	raw = request(rootClient, root, http.MethodPost, callPath, body, http.StatusOK)
	var result mcpclient.Result
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Content) != 1 || result.Content[0].Text != "echoed" || string(result.StructuredContent) != `{"verified":true}` {
		t.Fatalf("unexpected Computer MCP result: %s %v", raw, err)
	}
	request(otherClient, other, http.MethodPost, callPath, body, http.StatusServiceUnavailable)
	p.Harness.ReleaseACPMounts("mcp-gateway-owner")
	request(rootClient, root, http.MethodPost, callPath, body, http.StatusServiceUnavailable)
	if calls.Load() != 1 {
		t.Fatalf("unauthorized or detached command reached the remote tool: calls=%d", calls.Load())
	}
}

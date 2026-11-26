package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testRPC struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func parseTestRPC(t *testing.T, request *http.Request) testRPC {
	t.Helper()
	var rpc testRPC
	if request.Method != http.MethodPost || json.NewDecoder(request.Body).Decode(&rpc) != nil {
		t.Errorf("invalid MCP request %s", request.Method)
	}
	return rpc
}

func initResult(version string) map[string]any {
	return map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": struct{}{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
}

func responseJSON(id json.RawMessage, result any) []byte {
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		panic(err)
	}
	return data
}

func overLimitTools() []Tool {
	tools := make([]Tool, MaxTools+1)
	for i := range tools {
		tools[i] = testTool(fmt.Sprintf("fixture_%d", i))
	}
	return tools
}

func writeJSON(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(responseJSON(id, result))
}

func testTool(name string) Tool {
	return Tool{Name: name, Description: "Fixture tool", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func TestStreamableHTTPInitializationPaginationAndResume(t *testing.T) {
	const secret = "Bearer test-only-authorization"
	const sessionID = "http-fixture-session"
	const selectedVersion = "2025-06-18"
	var callID atomic.Pointer[json.RawMessage]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != secret {
			t.Errorf("missing configured authorization")
		}
		if r.Method == http.MethodGet {
			if r.Header.Get("Last-Event-ID") != "result-stream" || r.Header.Get("Mcp-Session-Id") != sessionID || r.Header.Get("Mcp-Protocol-Version") != selectedVersion {
				t.Error("invalid SSE resume headers")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			id := callID.Load()
			if id == nil {
				t.Error("resumed before receiving tools/call")
				return
			}
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\n", *id)
			fmt.Fprint(w, "data: \"result\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}],\"structuredContent\":{\"count\":1}}}\n\n")
			return
		}
		if r.Header.Get("Accept") != "application/json, text/event-stream" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect Streamable HTTP content negotiation")
		}
		rpc := parseTestRPC(t, r)
		if rpc.Method != "initialize" && (r.Header.Get("Mcp-Session-Id") != sessionID || r.Header.Get("Mcp-Protocol-Version") != selectedVersion) {
			t.Errorf("missing negotiated headers on %s", rpc.Method)
		}
		switch rpc.Method {
		case "initialize":
			if r.Header.Get("Mcp-Session-Id") != "" || !strings.Contains(string(rpc.Params), `"protocolVersion":"2025-11-25"`) {
				t.Error("unexpected initial protocol negotiation")
			}
			w.Header().Set("Mcp-Session-Id", sessionID)
			writeJSON(w, rpc.ID, initResult(selectedVersion))
		case "notifications/initialized":
			if len(rpc.ID) != 0 {
				t.Error("notification included a request ID")
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if string(rpc.Params) == `{}` {
				writeJSON(w, rpc.ID, map[string]any{"tools": []Tool{testTool("read_file")}, "nextCursor": "page-2"})
				return
			}
			if string(rpc.Params) != `{"cursor":"page-2"}` {
				t.Error("wrong next tool cursor")
			}
			writeJSON(w, rpc.ID, map[string]any{"tools": []Tool{testTool("list_files")}})
		case "tools/call":
			if string(rpc.Params) != `{"name":"list_files","arguments":{"depth":1}}` {
				t.Error("incorrect tool arguments")
			}
			callID.Store(&rpc.ID)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "id: result-stream\nretry: 0\ndata:\n\n")
		default:
			t.Error("unexpected MCP request")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	setup, cancel := context.WithCancel(context.Background())
	c, err := Connect(setup, ServerSpec{Type: "http", URL: server.URL, Headers: []Header{{Name: "Authorization", Value: secret}}}, Options{})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tools := c.Tools()
	if len(tools) != 2 || tools[0].Name != "read_file" || tools[1].Name != "list_files" {
		t.Fatalf("tools = %+v", tools)
	}
	tools[0].InputSchema[0] = '!'
	if c.Tools()[0].InputSchema[0] != '{' {
		t.Fatal("caller mutated the stored schema")
	}
	result, err := c.Call(t.Context(), "list_files", json.RawMessage(`{"depth":1}`))
	if err != nil || len(result.Content) != 1 || result.Content[0].Text != "done" || string(result.StructuredContent) != `{"count":1}` {
		t.Fatalf("call = %+v, %v", result, err)
	}
}

func TestLegacySSEDispatchAndClose(t *testing.T) {
	responses := make(chan []byte)
	leaseClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/events":
			defer close(leaseClosed)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": heartbeat\r\n\r\nevent: endpoint\r\ndata: /rpc?session=fixture\r\n\r\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case response := <-responses:
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", response)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		case "/rpc":
			if r.URL.Query().Get("session") != "fixture" {
				t.Error("legacy endpoint discarded its query")
			}
			rpc := parseTestRPC(t, r)
			var result any
			switch rpc.Method {
			case "initialize":
				result = initResult(legacyVersion)
			case "tools/list":
				result = map[string]any{"tools": []Tool{testTool("count")}}
			case "tools/call":
				result = Result{Content: []Content{{Type: "text", Text: string(rpc.Params)}}}
			}
			if result != nil {
				select {
				case responses <- responseJSON(rpc.ID, result):
				case <-r.Context().Done():
				}
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	c, err := Connect(t.Context(), ServerSpec{Type: "sse", URL: server.URL + "/events"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Parallel subtests complete before this cleanup runs.
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
		select {
		case <-leaseClosed:
		case <-time.After(time.Second):
			t.Error("legacy stream remained open")
		}
	})
	for i := 1; i <= 2; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			arguments := fmt.Appendf(nil, `{"count":%d}`, i)
			result, err := c.Call(t.Context(), "count", arguments)
			if err != nil || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, string(arguments)) {
				t.Fatalf("legacy call = %+v, %v", result, err)
			}
		})
	}
}

func TestCallCancellationAndClientRevocation(t *testing.T) {
	entered := make(chan json.RawMessage, 2)
	serverCanceled := make(chan string, 2)
	server := toolFixture(t, func(w http.ResponseWriter, r *http.Request, rpc testRPC) {
		switch rpc.Method {
		case "tools/call":
			entered <- rpc.ID
			<-r.Context().Done()
		case "notifications/cancelled":
			serverCanceled <- string(rpc.Params)
			w.WriteHeader(http.StatusAccepted)
		}
	})
	defer server.Close()
	c, err := Connect(t.Context(), ServerSpec{Type: "http", URL: server.URL}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := c.Call(ctx, "fixture", nil); finished <- err }()
	var id json.RawMessage
	select {
	case id = <-entered:
	case <-time.After(time.Second):
		t.Fatal("tool call did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool call did not cancel")
	}
	select {
	case params := <-serverCanceled:
		if params != fmt.Sprintf(`{"requestId":%s}`, id) {
			t.Errorf("cancelled wrong request: %s", params)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel notification was not sent")
	}
	go func() { _, err := c.Call(t.Context(), "fixture", nil); finished <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("second tool did not start")
	}
	_ = c.Close()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel the tool call")
	}
	if _, err := c.Call(t.Context(), "fixture", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("revoked client call: %v", err)
	}
}

func TestMCPRejectsRedirectsAndCrossOriginLegacyEndpoints(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	spec := ServerSpec{Type: "http", URL: redirect.URL, Headers: []Header{{Name: "Authorization", Value: "Bearer fixture-token"}}}
	if _, err := Connect(t.Context(), spec, Options{}); !errors.Is(err, ErrDestination) {
		t.Fatalf("redirect policy: %v", err)
	}
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", target.URL)
	}))
	defer legacy.Close()
	spec.Type, spec.URL = "sse", legacy.URL
	if _, err := Connect(t.Context(), spec, Options{}); !errors.Is(err, ErrDestination) {
		t.Fatalf("legacy origin policy: %v", err)
	}
	if targetCalls.Load() != 0 {
		t.Fatal("denied destination received a request")
	}
}

func TestMCPAcceptsLocalAndPrivateDestinations(t *testing.T) {
	transport := newTransport()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// A cancelled dial tests each destination without sending network traffic.
	for _, host := range []string{"127.0.0.1", "::1", "192.168.1.9", "100.65.0.1", "fd7a:115c:a1e0::1"} {
		address := net.JoinHostPort(host, "8080")
		if _, err := parseEndpoint("http://" + address); err != nil {
			t.Errorf("endpoint %s: %v", address, err)
		}
		if conn, err := transport.DialContext(ctx, "tcp", address); !errors.Is(err, context.Canceled) {
			if conn != nil {
				_ = conn.Close()
			}
			t.Errorf("cancelled dial to %s: %v", address, err)
		}
	}
}

func TestMCPRejectsInvalidURLsAndHeaders(t *testing.T) {
	for _, address := range []string{"file:///tmp/mcp", "http:///mcp", "https://user:secret@localhost/mcp", "http://localhost/mcp#fragment", "http://local host/mcp"} {
		if _, err := parseEndpoint(address); !errors.Is(err, ErrDestination) {
			t.Errorf("endpoint %s: %v", address, err)
		}
	}
	for _, header := range []Header{{Name: "Host", Value: "localhost"}, {Name: "X Wrong", Value: "token"}, {Name: "Authorization", Value: "first\r\nsecond"}} {
		if _, err := connectionHeaders([]Header{header}); err == nil {
			t.Errorf("invalid header accepted: %s", header.Name)
		}
	}
}

func TestMCPToolAndResponseLimitsAndRemoteErrors(t *testing.T) {
	const credential = "mcp-private-auth-abc"
	const headerValue = "Bearer " + credential
	var mode atomic.Int32
	server := toolFixture(t, func(w http.ResponseWriter, r *http.Request, rpc testRPC) {
		switch mode.Load() {
		case 0:
			writeJSON(w, rpc.ID, Result{Content: []Content{{Type: "text", Text: strings.Repeat("x", MaxResultBytes)}}})
		case 1:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":%q,"data":%q}}`, rpc.ID, credential, credential)
		case 2:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"Bearer mcp-private-auth-\u0061bc"}]}}`, rpc.ID)
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(credential))
		}
	})
	defer server.Close()
	c, err := Connect(t.Context(), ServerSpec{Type: "http", URL: server.URL, Headers: []Header{{Name: "Authorization", Value: headerValue}}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i, want := range []error{ErrLimit, ErrRemote, nil, ErrRemote} {
		mode.Store(int32(i))
		result, err := c.Call(t.Context(), "fixture", nil)
		if want == nil {
			if err != nil || len(result.Content) != 1 || result.Content[0].Text != headerValue {
				t.Errorf("case %d did not preserve the matching result: %+v, %v", i, result, err)
			}
		} else if !errors.Is(err, want) || strings.Contains(err.Error(), credential) || len(result.Content) != 0 {
			t.Errorf("case %d returned incorrect error or exposed response data", i)
		}
	}
	for _, tc := range []struct {
		name  string
		tools []Tool
	}{
		{name: "tool_count", tools: overLimitTools()},
		{name: "schema_size", tools: []Tool{{Name: "fixture", InputSchema: fmt.Appendf(nil, `{"description":%q}`, strings.Repeat("x", MaxSchemaBytes))}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rpc := parseTestRPC(t, r)
				switch rpc.Method {
				case "initialize":
					writeJSON(w, rpc.ID, initResult(streamableVersion))
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
				case "tools/list":
					writeJSON(w, rpc.ID, map[string]any{"tools": tc.tools})
				}
			}))
			defer server.Close()
			if _, err := Connect(t.Context(), ServerSpec{Type: "http", URL: server.URL}, Options{}); !errors.Is(err, ErrLimit) {
				t.Fatalf("wrong tool admission error: %v", err)
			}
		})
	}
}

func toolFixture(t *testing.T, other func(http.ResponseWriter, *http.Request, testRPC)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpc := parseTestRPC(t, r)
		switch rpc.Method {
		case "initialize":
			writeJSON(w, rpc.ID, initResult(streamableVersion))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeJSON(w, rpc.ID, map[string]any{"tools": []Tool{testTool("fixture")}})
		default:
			other(w, r, rpc)
		}
	}))
}

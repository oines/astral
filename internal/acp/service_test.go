package acp

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/schedule"
	"github.com/oines/astral/internal/testkit"
)

type wireMessage struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Error  *rpcError
	Result struct {
		ProtocolVersion int    `json:"protocolVersion"`
		SessionID       string `json:"sessionId"`
		StopReason      string `json:"stopReason"`
	} `json:"result"`
	Params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind    string            `json:"sessionUpdate"`
			Content json.RawMessage   `json:"content"`
			Meta    map[string]string `json:"_meta"`
			ToolID  string            `json:"toolCallId"`
			Title   string            `json:"title"`
			Status  string            `json:"status"`
		} `json:"update"`
	} `json:"params"`
}

type testClient struct {
	conn *websocket.Conn
	ctx  context.Context
}

func dial(t *testing.T, url, token string) (testClient, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	conn, response, err := websocket.Dial(ctx, strings.Replace(url, "http://", "ws://", 1), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err == nil {
		t.Cleanup(func() { _ = conn.CloseNow() })
	}
	return testClient{conn: conn, ctx: ctx}, response, err
}

func (client testClient) send(t *testing.T, id int, method string, params any) {
	t.Helper()
	message := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id != 0 {
		message["id"] = id
	}
	if err := wsjson.Write(client.ctx, client.conn, message); err != nil {
		t.Fatal(err)
	}
}

func (client testClient) read(t *testing.T) wireMessage {
	t.Helper()
	var message wireMessage
	if err := wsjson.Read(client.ctx, client.conn, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func (client testClient) response(t *testing.T, id int) (wireMessage, []wireMessage) {
	t.Helper()
	var updates []wireMessage
	for {
		message := client.read(t)
		if message.ID != 0 {
			if message.ID != id {
				t.Fatalf("RPC response %d arrived while waiting for %d", message.ID, id)
			}
			return message, updates
		}
		updates = append(updates, message)
	}
}

func (client testClient) initialize(t *testing.T) {
	t.Helper()
	client.send(t, 1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}})
	response, _ := client.response(t, 1)
	if response.Error != nil || response.Result.ProtocolVersion != 1 {
		t.Fatalf("initialize: %+v", response)
	}
}

func (client testClient) create(t *testing.T) string {
	t.Helper()
	client.send(t, 2, "session/new", map[string]any{"cwd": "/work", "mcpServers": []any{}})
	response, _ := client.response(t, 2)
	if response.Error != nil || response.Result.SessionID == "" {
		t.Fatalf("session/new: %+v", response)
	}
	return response.Result.SessionID
}

func updateCount(updates []wireMessage, kind, text, source string) int {
	count := 0
	for _, message := range updates {
		update := message.Params.Update
		var content contentBlock
		if message.Method == "session/update" && update.Kind == kind && update.Meta["trigger_source"] == source && json.Unmarshal(update.Content, &content) == nil && content.Text == text {
			count++
		}
	}
	return count
}

func TestACPWebSocketSessionsAndTokenRevocation(t *testing.T) {
	const tokenName = `Editor "quoted" <Desk> & Lab`
	const acpQuestion = "ACP question <task> & details"
	const webQuestion = "Web follow-up <task> & details"
	providerRequests := make(chan map[string]any, 4)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		// Exclude the separate title request and capture actual conversation input.
		if request["tools"] != nil {
			select {
			case providerRequests <- request:
			default:
				t.Error("more conversation requests than expected")
			}
		}
		return testkit.Text("bot answer")
	})
	service := New(p.Harness)
	server := httptest.NewServer(service)
	defer server.Close()
	token, err := service.CreateToken(p.Context, p.Agent.ID, "  "+tokenName+"  ")
	if err != nil || token.Name != tokenName || token.ID == "" {
		t.Fatalf("create token: %+v, %v", token.TokenInfo, err)
	}
	checkModelInput := func(text, source, clientName string) {
		t.Helper()
		var request map[string]any
		select {
		case request = <-providerRequests:
		default:
			t.Fatal("no model request received for", source)
		}
		messages, _ := request["messages"].([]any)
		if strings.Contains(string(testkit.JSON(messages)), token.Token) {
			t.Fatal("the model request contains the ACP bearer token")
		}
		for _, raw := range messages {
			message, _ := raw.(map[string]any)
			if message["role"] != "user" {
				continue
			}
			parts, _ := message["content"].([]any)
			for _, rawPart := range parts {
				part, _ := rawPart.(map[string]any)
				content, _ := part["text"].(string)
				if part["type"] != "text" || !strings.HasPrefix(content, "<message ") {
					continue
				}
				var input struct {
					XMLName  xml.Name `xml:"message"`
					Source   string   `xml:"source,attr"`
					Sender   string   `xml:"sender,attr"`
					SenderID string   `xml:"sender-id,attr"`
					Client   string   `xml:"client,attr"`
					Text     string   `xml:",chardata"`
				}
				if err := xml.Unmarshal([]byte(content), &input); err != nil {
					t.Fatalf("invalid XML in the user message envelope: %v", err)
				}
				if strings.TrimSpace(input.Text) != text {
					continue
				}
				if input.Source != source || input.Sender != "Owner" || input.SenderID != "owner" || input.Client != clientName {
					t.Errorf("unexpected model-visible user identity: %+v", input)
				}
				return
			}
		}
		t.Fatalf("model request did not include %s user message %q", source, text)
	}
	rows, err := p.Store.List(p.Context, tokenKind)
	if err != nil || len(rows) != 1 || strings.Contains(string(rows[0].Data), token.Token) {
		t.Fatal("plaintext credential was retained or token is missing")
	}
	if _, response, err := dial(t, server.URL, "invalid"); err == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized upgrade, received %v, %v", response, err)
	}
	client, upgrade, err := dial(t, server.URL, token.Token)
	if err != nil {
		t.Fatal(err)
	}
	connectionID := upgrade.Header.Get("Acp-Connection-Id")
	if connectionID == "" {
		t.Fatal("missing connection identity")
	}
	client.initialize(t)
	listed, err := service.ListTokens(p.Context, p.Agent.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != token.ID || listed[0].LastUsedAt == nil {
		t.Fatalf("list tokens: %+v, %v", listed, err)
	}
	if mustReply := func() wireMessage {
		client.send(t, 11, "session/new", map[string]any{"cwd": "/work", "mcpServers": []any{map[string]any{"name": "shell", "command": "/bin/false"}}})
		response, _ := client.response(t, 11)
		return response
	}(); mustReply.Error == nil || mustReply.Error.Code != -32602 {
		t.Fatal("ACP accepted a stdio MCP server")
	}
	sessionID := client.create(t)
	client.send(t, 3, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": []contentBlock{{Type: "text", Text: acpQuestion}}})
	response, updates := client.response(t, 3)
	if response.Error != nil || response.Result.StopReason != "end_turn" || updateCount(updates, "agent_message_chunk", "bot answer", "acp") != 1 || updateCount(updates, "user_message_chunk", acpQuestion, "acp") != 0 {
		t.Fatalf("ACP prompt result=%+v, updates=%+v", response, updates)
	}
	checkModelInput(acpQuestion, "acp", tokenName)
	// A Web caller can supply any input ID. An ACP-shaped ID must neither
	// change the stored source nor suppress this message for the ACP observer.
	if _, err := p.Harness.Submit(p.Context, sessionID, harness.InputRequest{ID: connectionID + ":from-web", Text: webQuestion}); err != nil {
		t.Fatal(err)
	}
	updates = nil
	for {
		message := client.read(t)
		updates = append(updates, message)
		if message.Params.Update.Kind == "agent_message_chunk" {
			break
		}
	}
	if updateCount(updates, "user_message_chunk", webQuestion, "web") != 1 || updateCount(updates, "agent_message_chunk", "bot answer", "web") != 1 {
		t.Fatalf("persistent ACP subscription did not receive the Web submission: %+v", updates)
	}
	checkModelInput(webQuestion, "web", "")
	p.Idle(t, sessionID)
	at := time.Now().UTC().Add(time.Hour)
	plan, err := p.Harness.Schedules.Create(p.Context, p.Agent.ID, schedule.Input{
		SessionID: sessionID, Title: "Scheduled check", Prompt: "Scheduled question", Enabled: true,
		Timing: domain.ScheduleTiming{Kind: "once", At: &at},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Harness.Schedules.Step(p.Context, *plan.NextRunAt); err != nil {
		t.Fatal(err)
	}
	updates = nil
	for updateCount(updates, "agent_message_chunk", "bot answer", "schedule") == 0 {
		updates = append(updates, client.read(t))
	}
	if updateCount(updates, "user_message_chunk", "Scheduled question", "schedule") != 1 {
		t.Fatal("persistent ACP subscription did not receive the scheduled submission")
	}
	p.Idle(t, sessionID)
	another, _, err := dial(t, server.URL, token.Token)
	if err != nil {
		t.Fatal(err)
	}
	another.initialize(t)
	another.send(t, 4, "session/load", map[string]any{"sessionId": sessionID, "cwd": "/work", "mcpServers": []any{}})
	response, updates = another.response(t, 4)
	if response.Error != nil || updateCount(updates, "user_message_chunk", acpQuestion, "acp") != 1 || updateCount(updates, "user_message_chunk", webQuestion, "web") != 1 || updateCount(updates, "agent_message_chunk", "bot answer", "web") != 1 || updateCount(updates, "user_message_chunk", "Scheduled question", "schedule") != 1 {
		t.Fatalf("ACP load response=%+v, replay=%+v", response, updates)
	}
	foreign := domain.Agent{Name: "Other Bot", DefaultModelID: p.Profile.ID}
	if err := p.Harness.SaveAgent(p.Context, &foreign); err != nil {
		t.Fatal(err)
	}
	otherSession, err := p.Harness.CreateSession(p.Context, foreign.ID, "Other conversation")
	if err != nil {
		t.Fatal(err)
	}
	another.send(t, 5, "session/load", map[string]any{"sessionId": otherSession.ID, "cwd": "/work", "mcpServers": []any{}})
	if response, _ := another.response(t, 5); response.Error == nil || response.Error.Code != -32002 {
		t.Fatalf("cross-Bot session load got %+v", response)
	}
	if err := service.RevokeToken(p.Context, p.Agent.ID, token.ID); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []testClient{client, another} {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		for {
			_, _, err := peer.conn.Read(ctx)
			if err != nil {
				if ctx.Err() != nil {
					t.Fatal("token revocation left a connection open")
				}
				break
			}
		}
		cancel()
	}
	if _, response, err := dial(t, server.URL, token.Token); err == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked credential accepted: %v, %v", response, err)
	}
}

func TestACPCancelTerminatesOriginalPrompt(t *testing.T) {
	started := make(chan struct{}, 1)
	p := testkit.New(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if !strings.Contains(string(testkit.JSON(request["messages"])), "cancel this work") || request["tools"] == nil {
			return testkit.Text("title")
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil
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
	sessionID := client.create(t)
	client.send(t, 3, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": []contentBlock{{Type: "text", Text: "cancel this work"}}})
	select {
	case <-started:
	case <-client.ctx.Done():
		t.Fatal(client.ctx.Err())
	}
	client.send(t, 0, "session/cancel", map[string]string{"sessionId": sessionID})
	response, _ := client.response(t, 3)
	if response.Error != nil || response.Result.StopReason != "cancelled" {
		t.Fatalf("cancelled prompt returned %+v", response)
	}
}

type mcpEchoFixture struct {
	url         string
	discoveries atomic.Int64
	calls       atomic.Int64
}

func newMCPEcho(t *testing.T) *mcpEchoFixture {
	t.Helper()
	fixture := &mcpEchoFixture{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("remote MCP credentials were missing")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": struct{}{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			fixture.discoveries.Add(1)
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Echo text", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			fixture.calls.Add(1)
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": "remote result"}}}
		default:
			t.Errorf("unexpected method %q", request.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rpcReply{JSONRPC: "2.0", ID: request.ID, Result: result})
	}))
	t.Cleanup(remote.Close)
	fixture.url = remote.URL
	return fixture
}

func (f *mcpEchoFixture) spec(name string) []any {
	return []any{map[string]any{
		"name": name, "type": "http", "url": f.url,
		"headers": []map[string]string{{"name": "Authorization", "value": "Bearer fixture-secret"}},
	}}
}

func TestACPMountRemainsVisibleToSubscribedPeer(t *testing.T) {
	const toolName = "mcp__fixture__echo"
	fixture := newMCPEcho(t)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(testkit.JSON(request["tools"])), toolName) {
			if !strings.Contains(string(testkit.JSON(request["messages"])), "remote result") {
				return testkit.Tool(toolName, map[string]string{"value": "example"})
			}
			return testkit.Text("completed remote tool")
		}
		return testkit.Text("title")
	})
	service := New(p.Harness)
	server := httptest.NewServer(service)
	defer server.Close()
	token, err := service.CreateToken(p.Context, p.Agent.ID, "Editor")
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := dial(t, server.URL, token.Token)
	if err != nil {
		t.Fatal(err)
	}
	owner.initialize(t)
	sessionID := owner.create(t)
	observer, _, err := dial(t, server.URL, token.Token)
	if err != nil {
		t.Fatal(err)
	}
	observer.initialize(t)
	observer.send(t, 3, "session/load", map[string]any{"sessionId": sessionID, "cwd": "/work"})
	if response, _ := observer.response(t, 3); response.Error != nil {
		t.Fatalf("observer load: %+v", response)
	}
	owner.send(t, 4, "session/load", map[string]any{"sessionId": sessionID, "cwd": "/work", "mcpServers": fixture.spec("fixture")})
	if response, _ := owner.response(t, 4); response.Error != nil {
		t.Fatalf("owner MCP load: %+v", response)
	}
	owner.send(t, 5, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": []contentBlock{{Type: "text", Text: "Use the echo tool."}}})
	if response, _ := owner.response(t, 5); response.Error != nil || response.Result.StopReason != "end_turn" {
		t.Fatalf("MCP prompt: %+v", response)
	}
	var updates []wireMessage
	for updateCount(updates, "agent_message_chunk", "completed remote tool", "acp") == 0 {
		updates = append(updates, observer.read(t))
	}
	var toolID string
	var completed bool
	for _, update := range updates {
		part := update.Params.Update
		if part.Kind == "tool_call" && part.Title == toolName {
			toolID = part.ToolID
		}
		if toolID != "" && part.ToolID == toolID && part.Status == "completed" {
			completed = true
		}
	}
	if fixture.calls.Load() != 1 || !completed || updateCount(updates, "user_message_chunk", "Use the echo tool.", "acp") != 1 {
		t.Fatalf("MCP sync calls=%d, completed=%v, updates=%+v", fixture.calls.Load(), completed, updates)
	}
}

func TestACPFailedLoadPreservesConnectionAndExistingMounts(t *testing.T) {
	fixture := newMCPEcho(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("bot answer") })
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
	load := func(rpcID int, sessionID string, servers []any) wireMessage {
		t.Helper()
		client.send(t, rpcID, "session/load", map[string]any{"sessionId": sessionID, "cwd": "/work", "mcpServers": servers})
		response, _ := client.response(t, rpcID)
		return response
	}
	sessionA := client.create(t)
	if response := load(3, sessionA, fixture.spec("session_a")); response.Error != nil {
		t.Fatalf("load A: %+v", response)
	}
	sessionB := client.create(t)
	if response := load(4, sessionB, fixture.spec("session_b")); response.Error != nil {
		t.Fatalf("load B: %+v", response)
	}
	if _, err := p.Harness.Submit(p.Context, sessionB, harness.InputRequest{ID: "seed-b", Text: "Seed history for B"}); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, sessionB)
	// The existing runtime and Session remain usable, but a new load will
	// fail replay when it cannot reach the saved fork point in its history.
	var record domain.Session
	if err := p.Store.Get(p.Context, "sessions", sessionB, &record); err != nil {
		t.Fatal(err)
	}
	record.ForkEntryID = "missing-fork-point"
	if err := p.Store.Save(p.Context, "sessions", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []struct {
		rpcID       int
		servers     []any
		discoveries int64
	}{
		{rpcID: 5, servers: []any{}, discoveries: 2},
		{rpcID: 6, servers: fixture.spec("replacement"), discoveries: 3},
	} {
		if response := load(attempt.rpcID, sessionB, attempt.servers); response.Error == nil {
			t.Fatalf("broken B unexpectedly loaded: %+v", response)
		}
		if got := fixture.discoveries.Load(); got != attempt.discoveries {
			t.Fatalf("tool discoveries after failed load: got %d, want %d", got, attempt.discoveries)
		}
	}
	for _, existing := range []struct{ sessionID, tool string }{
		{sessionA, "mcp__session_a__echo"},
		{sessionB, "mcp__session_b__echo"},
	} {
		tools, err := p.Harness.MCPTools(p.Context, existing.sessionID)
		if err != nil || len(tools) != 1 || tools[0].Name != existing.tool {
			t.Fatalf("original mount missing after failed load: %+v, %v", tools, err)
		}
		result, err := p.Harness.CallMCPTool(p.Context, existing.sessionID, existing.tool, json.RawMessage(`{}`))
		if err != nil || len(result.Content) != 1 || result.Content[0].Text != "remote result" {
			t.Fatalf("original tool cannot be called: %+v, %v", result, err)
		}
		client.send(t, 7, "session/prompt", map[string]any{"sessionId": existing.sessionID, "prompt": []contentBlock{{Type: "text", Text: "Still subscribed"}}})
		response, _ := client.response(t, 7)
		if response.Error != nil || response.Result.StopReason != "end_turn" {
			t.Fatalf("existing subscription stopped working: %+v", response)
		}
	}
}

func TestJSONRPCRejectsInvalidRequests(t *testing.T) {
	for _, raw := range []string{`[`, `[]`, `null`, `{"jsonrpc":"2.0","method":"session/new","id":{}}`} {
		if _, err := decodeRequest(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

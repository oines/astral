package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

func secondSessionBot(t *testing.T, p *testkit.Platform, server *httptest.Server) (domain.Agent, string) {
	t.Helper()
	bot := domain.Agent{
		Name: "Other Bot", DefaultModelID: p.Profile.ID,
		AllowedComputers: p.Agent.AllowedComputers, DefaultComputerID: p.Agent.DefaultComputerID,
	}
	if err := p.Harness.SaveAgent(p.Context, &bot); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/agents/" + bot.ID + "/bot-api-tokens"
	response := request(t, p.Context, server, http.MethodPost, path, strings.NewReader(`{"name":"Other session reader"}`), true)
	defer response.Body.Close()
	var result struct{ Token string }
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode/100 != 2 || result.Token == "" {
		t.Fatalf("create other Bot token: HTTP%d, error %v", response.StatusCode, err)
	}
	return bot, result.Token
}

func createBotHTTPSession(t *testing.T, p *testkit.Platform, server *httptest.Server, token, botID, title string) harness.View {
	t.Helper()
	raw := resourceBotRequest(t, p, server, token, "POST", "/sessions", SessionInput{Title: title}, 200)
	var view harness.View
	if err := json.Unmarshal(raw, &view); err != nil || view.ID == "" || view.AgentID != botID || view.Title != title {
		t.Fatalf("Bot could not create its own session: %s, error %v", raw, err)
	}
	return view
}

func testBotSessionResponse(t *testing.T, ctx context.Context, server *httptest.Server, token, method, path string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, server.URL+BotAPIPrefix+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestBotSessionCreationDirectoryAndHistoryIsolation(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("Finished work") })
	_, server := apiFixture(t, p)
	token := resourceBotToken(t, p, server)
	other, otherToken := secondSessionBot(t, p, server)
	root := createBotHTTPSession(t, p, server, token, p.Agent.ID, "My history")
	alternate := createBotHTTPSession(t, p, server, token, p.Agent.ID, "My second session")
	foreign := createBotHTTPSession(t, p, server, otherToken, other.ID, "Other Bot history")
	hidden := createBotHTTPSession(t, p, server, token, p.Agent.ID, "Owner-only session")
	private := true
	if err := p.Harness.SaveSessionPresentation(p.Context, hidden.ID, nil, nil, nil, &private); err != nil {
		t.Fatal(err)
	}
	resourceBotRequest(t, p, server, token, "POST", "/sessions", SessionInput{AgentID: other.ID, Title: "Forged ownership"}, 403)
	resourceBotRequest(t, p, server, token, "GET", "/sessions/"+foreign.ID, nil, 404)
	resourceBotRequest(t, p, server, token, "GET", "/sessions/"+hidden.ID, nil, 404)

	for _, viewer := range []struct {
		token string
		ids   []string
	}{
		{token: token, ids: []string{p.Session.ID, root.ID, alternate.ID}},
		{token: otherToken, ids: []string{foreign.ID}},
	} {
		raw := resourceBotRequest(t, p, server, viewer.token, "GET", "/sessions", nil, 200)
		var sessions []harness.SessionSummary
		if err := json.Unmarshal(raw, &sessions); err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(sessions))
		for _, session := range sessions {
			ids = append(ids, session.ID)
		}
		slices.Sort(ids)
		slices.Sort(viewer.ids)
		if !slices.Equal(ids, viewer.ids) {
			t.Fatalf("directory returned %v, want %v", ids, viewer.ids)
		}
	}

	history := "/sessions/" + root.ID + "/history"
	resourceBotRequest(t, p, server, token, "POST", "/sessions/"+root.ID+"/inputs", harness.InputRequest{ID: "history-fixture", Text: "One user turn with a model answer"}, 200)
	p.Idle(t, root.ID)
	raw := resourceBotRequest(t, p, server, token, "GET", history+"?limit=1", nil, 200)
	var first gateway.HistoryPage
	if err := json.Unmarshal(raw, &first); err != nil || len(first.Entries) != 1 || first.NextCursor == "" {
		t.Fatalf("Bot history first page: %s, error %v", raw, err)
	}
	query := "?limit=1&cursor=" + url.QueryEscape(first.NextCursor)
	raw = resourceBotRequest(t, p, server, token, "GET", history+query, nil, 200)
	var second gateway.HistoryPage
	if err := json.Unmarshal(raw, &second); err != nil || len(second.Entries) != 1 || second.Entries[0].ID == first.Entries[0].ID {
		t.Fatalf("Bot cursor did not advance: %s, error %v", raw, err)
	}
	resourceBotRequest(t, p, server, token, "GET", "/sessions/"+alternate.ID+"/history"+query, nil, 400)
	resourceBotRequest(t, p, server, otherToken, "GET", "/sessions/"+foreign.ID+"/history"+query, nil, 400)
	resourceBotRequest(t, p, server, otherToken, "GET", history+query, nil, 404)
	resourceBotRequest(t, p, server, token, "GET", history+query+"&trace=true", nil, 400)
	resourceBotRequest(t, p, server, token, "GET", "/sessions/"+root.ID+"/timeline"+query, nil, 400)

	state, err := p.CoreStore.LoadState(p.Context, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	position, err := p.CoreStore.Scan(p.Context, state.RolloutID, core.Query{PathHead: state.Branch.HeadID, HistoryOnly: true, Limit: 1})
	if err != nil || position.Next == "" || position.Next == first.NextCursor {
		t.Fatalf("did not obtain a raw Core cursor: error %v", err)
	}
	for _, id := range []string{alternate.ID, foreign.ID} {
		otherState, err := p.CoreStore.LoadState(p.Context, id)
		if err != nil || otherState.RolloutID == state.RolloutID {
			t.Fatalf("session %s does not have a separate rollout: error %v", id, err)
		}
	}
	rawQuery := "?limit=1&cursor=" + url.QueryEscape(position.Next)
	resourceBotRequest(t, p, server, token, "GET", history+rawQuery, nil, 400)
	resourceBotRequest(t, p, server, token, "GET", "/sessions/"+alternate.ID+"/history"+rawQuery, nil, 400)
	resourceBotRequest(t, p, server, otherToken, "GET", "/sessions/"+foreign.ID+"/history"+rawQuery, nil, 400)
}

func TestBotSessionAttachmentDoesNotCrossBotBoundary(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	token := resourceBotToken(t, p, server)
	other, otherToken := secondSessionBot(t, p, server)
	foreign := createBotHTTPSession(t, p, server, otherToken, other.ID, "Other attachment reader")
	upload := "/sessions/" + p.Session.ID + "/attachments?name=fixture.txt"
	const content = "Attachment owned by the first Bot"
	response := testBotSessionResponse(t, p.Context, server, token, "POST", upload, strings.NewReader(content))
	var file UploadedAttachment
	err := json.NewDecoder(response.Body).Decode(&file)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || file.File.ID == "" || file.File.AgentID != p.Agent.ID || file.URI == "" {
		t.Fatalf("Bot upload: HTTP%d, file %+v, error %v", response.StatusCode, file.File, err)
	}
	response = testBotSessionResponse(t, p.Context, server, otherToken, "POST", upload, strings.NewReader("forbidden"))
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("cross-Bot upload: HTTP%d", response.StatusCode)
	}
	refs, err := p.Store.List(p.Context, "files")
	if err != nil || len(refs) != 1 || refs[0].ID != file.File.ID {
		t.Fatalf("cross-Bot upload changed the file directory: %+v, error %v", refs, err)
	}
	path := "/files/" + file.File.ID
	raw := resourceBotRequest(t, p, server, token, "GET", path+"/content?session_id="+p.Session.ID, nil, 200)
	if !bytes.Equal(raw, []byte(content)) {
		t.Fatalf("allowed download returned %q", raw)
	}
	// Both Bots have access to the same physical Computer. The file owner
	// still has to match, even when the other Bot supplies its own session.
	resourceBotRequest(t, p, server, otherToken, "GET", path+"/content?session_id="+foreign.ID, nil, 404)
	resourceBotRequest(t, p, server, otherToken, "GET", path+"/content?session_id="+p.Session.ID, nil, 404)
	resourceBotRequest(t, p, server, otherToken, "GET", path+"?session_id="+foreign.ID, nil, 404)
}

func TestBotAPIUploadCanBeUsedAsACPImage(t *testing.T) {
	var sawImage atomic.Bool
	p := testkit.New(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		if bytes.Contains(testkit.JSON(request["messages"]), []byte("data:image/")) {
			sawImage.Store(true)
		}
		return testkit.Text("Received the image")
	})
	api, server := apiFixture(t, p)
	token := resourceBotToken(t, p, server)
	img := image.NewNRGBA(image.Rect(0, 0, 640, 640))
	var content bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&content, img); err != nil {
		t.Fatal(err)
	}
	if content.Len() <= 1<<20 {
		t.Fatalf("expected an upload larger than the ACP message limit, got %d bytes", content.Len())
	}
	response := testBotSessionResponse(t, p.Context, server, token, "POST", "/sessions/"+p.Session.ID+"/attachments?name=diagram.png", &content)
	var attachment UploadedAttachment
	err := json.NewDecoder(response.Body).Decode(&attachment)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || attachment.URI == "" {
		t.Fatalf("upload image: HTTP%d, error %v", response.StatusCode, err)
	}

	acpToken, err := api.Config.ACP.CreateToken(p.Context, p.Agent.ID, "ACP client")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(p.Context, 20*time.Second)
	defer cancel()
	socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/acp", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + acpToken.Token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	call := func(id int, method string, params any) json.RawMessage {
		t.Helper()
		if err := wsjson.Write(ctx, socket, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for {
			var data struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := wsjson.Read(ctx, socket, &data); err != nil {
				t.Fatal(err)
			}
			if data.ID == id {
				if len(data.Error) != 0 {
					t.Fatalf("%s: %s", method, data.Error)
				}
				return data.Result
			}
		}
	}
	call(1, "initialize", map[string]int{"protocolVersion": 1})
	call(2, "session/resume", map[string]any{"sessionId": p.Session.ID, "cwd": p.Session.CWD, "mcpServers": []any{}})
	result := call(3, "session/prompt", map[string]any{
		"sessionId": p.Session.ID,
		"prompt":    []map[string]string{{"type": "resource_link", "name": "diagram.png", "uri": attachment.URI}},
	})
	if !sawImage.Load() || !bytes.Contains(result, []byte(`"end_turn"`)) {
		t.Fatalf("model did not receive the linked image: %s", result)
	}
}

func TestBotSubagentDetailAndLiveStreamFollowRootPrivacy(t *testing.T) {
	const childText = "Subagent live transcript test"
	started, release := make(chan struct{}), make(chan struct{})
	releaseWork := sync.OnceFunc(func() { close(release) })
	defer releaseWork()
	var spawn atomic.Bool
	var firstChild sync.Once
	p := testkit.New(t, 1, func(_ context.Context, _ int, prompt map[string]any) any {
		if prompt["tools"] == nil {
			return testkit.Text("Fixture title")
		}
		if strings.Contains(string(testkit.JSON(prompt["messages"])), "You are a subagent of Session") {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				firstChild.Do(func() { close(started) })
				select {
				case <-r.Context().Done():
				case <-release:
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(testkit.JSON(testkit.Text(childText)))
				}
			})
		}
		if spawn.CompareAndSwap(false, true) {
			return testkit.Tool("spawn_agent", map[string]string{"name": "Fixture child", "task": "Produce the test transcript"})
		}
		return testkit.Text("Parent session answer")
	})
	_, server := apiFixture(t, p)
	token := resourceBotToken(t, p, server)
	_, otherToken := secondSessionBot(t, p, server)
	parent := "/sessions/" + p.Session.ID
	resourceBotRequest(t, p, server, token, "POST", parent+"/inputs", harness.InputRequest{ID: "spawn-fixture", Text: "Delegate the work"}, 200)
	ctx, cancel := context.WithTimeout(p.Context, 15*time.Second)
	defer cancel()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("child model did not start:", ctx.Err())
	}

	raw := resourceBotRequest(t, p, server, token, "GET", parent+"/subagents", nil, 200)
	var children []harness.SessionSummary
	if err := json.Unmarshal(raw, &children); err != nil || len(children) != 1 || children[0].SubagentParentID != p.Session.ID {
		t.Fatalf("Bot subagent directory: %s, error %v", raw, err)
	}
	childID := children[0].ID
	child := "/sessions/" + childID
	raw = resourceBotRequest(t, p, server, token, "GET", child, nil, 200)
	var details harness.View
	if err := json.Unmarshal(raw, &details); err != nil || details.ID != childID || details.SubagentParentID != p.Session.ID {
		t.Fatalf("Bot subagent detail: %s, error %v", raw, err)
	}
	resourceBotRequest(t, p, server, otherToken, "GET", parent+"/subagents", nil, 404)
	resourceBotRequest(t, p, server, otherToken, "GET", child, nil, 404)
	resourceBotRequest(t, p, server, token, "POST", child+"/inputs", harness.InputRequest{ID: "not-accepted", Text: "Override delegated work"}, 403)

	assertNoStream := func(token string) {
		t.Helper()
		response := testBotSessionResponse(t, ctx, server, token, "GET", child+"/events", nil)
		response.Body.Close()
		if response.StatusCode != 404 {
			t.Fatalf("hidden subagent offered an SSE stream: HTTP%d", response.StatusCode)
		}
	}
	assertNoStream(otherToken)
	response := testBotSessionResponse(t, ctx, server, token, "GET", child+"/events", nil)
	if response.Header.Get("Content-Type") != "text/event-stream" {
		response.Body.Close()
		t.Fatalf("Bot subagent stream: HTTP%d, content type %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	stream := readInvalidationStream(t, ctx, response, cancel)
	select {
	case frame, ok := <-stream.frames:
		if !ok || frame.err != nil || frame.kind != "snapshot" || frame.snapshot == nil || frame.snapshot.View.ID != childID {
			t.Fatalf("missing child SSE snapshot: %+v", frame)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	releaseWork()
	for seen := false; !seen; {
		select {
		case frame, ok := <-stream.frames:
			if !ok || frame.err != nil {
				t.Fatalf("child stream stopped before showing the live answer: %+v", frame)
			}
			entry := frame.event.Entry
			seen = entry != nil && entry.SessionID == childID && bytes.Contains(testkit.JSON(entry.Message), []byte(childText))
		case <-ctx.Done():
			t.Fatal("no live child transcript:", ctx.Err())
		}
	}

	private := true
	if err := p.Harness.SaveSessionPresentation(p.Context, p.Session.ID, nil, nil, nil, &private); err != nil {
		t.Fatal(err)
	}
	for done := false; !done; {
		select {
		case _, open := <-stream.frames:
			done = !open
		case <-ctx.Done():
			t.Fatal("child stream stayed open after its root became private:", ctx.Err())
		}
	}
	resourceBotRequest(t, p, server, token, "GET", parent+"/subagents", nil, 404)
	resourceBotRequest(t, p, server, token, "GET", child, nil, 404)
	resourceBotRequest(t, p, server, token, "POST", child+"/inputs", harness.InputRequest{ID: "hidden", Text: "Still unauthorized"}, 404)
	assertNoStream(token)
	for _, inputID := range []string{"not-accepted", "hidden"} {
		if _, err := p.Harness.Receipt(p.Context, childID, inputID); err == nil {
			t.Fatalf("rejected child input %q was accepted", inputID)
		}
	}
}

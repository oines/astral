package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/mcpclient"
)

const mcpHarnessTool = "mcp__fixture__lookup"

func mcpHarnessFixture(t *testing.T, tools []mcpclient.Tool, call func(http.ResponseWriter, *http.Request, json.RawMessage)) mcpclient.ServerSpec {
	t.Helper()
	const secret = "Bearer mcp-harness-fixture-credential"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != secret {
			t.Error("MCP fixture did not receive its authorization")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var rpc struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			t.Error(err)
			return
		}
		var result any
		switch rpc.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": struct{}{}}}
		case "notifications/initialized", "notifications/cancelled":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			result = map[string]any{"tools": tools}
		case "tools/call":
			if call != nil {
				call(w, r, rpc.ID)
				return
			}
			if !bytes.Contains(rpc.Params, []byte(`"name":"lookup"`)) {
				t.Error("MCP tool call did not use its remote name")
			}
			result = mcpclient.Result{Content: []mcpclient.Content{{Type: "text", Text: "MCP fixture response"}}}
		default:
			t.Error("unexpected MCP method")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mcpHarnessJSON(w, rpc.ID, result)
	}))
	t.Cleanup(server.Close)
	return mcpclient.ServerSpec{Type: "http", Name: "fixture", URL: server.URL, Headers: []mcpclient.Header{{Name: "Authorization", Value: secret}}}
}

func mcpHarnessJSON(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func mcpHarnessDefinition(name, schema string) mcpclient.Tool {
	return mcpclient.Tool{Name: name, Description: "A fixture operation", InputSchema: json.RawMessage(schema)}
}

func attachMCPStreamModel(t *testing.T, f *fixture) func() {
	t.Helper()
	continueResponse := make(chan struct{})
	allowRest := sync.OnceFunc(func() { close(continueResponse) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Stream   bool              `json:"stream"`
			Tools    []json.RawMessage `json:"tools"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || !input.Stream || len(input.Messages) == 0 {
			t.Errorf("streaming model request failed: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mounted := false
		for _, tool := range input.Tools {
			mounted = mounted || bytes.Contains(tool, []byte(mcpHarnessTool))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if mounted && input.Messages[len(input.Messages)-1].Role != "tool" {
			fmt.Fprint(w, "data: "+`{"id":"first","choices":[{"index":0,"delta":{"role":"assistant","content":"First live delta"},"finish_reason":null}]}`+"\n\n")
			w.(http.Flusher).Flush()
			// The test must receive the first delta while the provider is still
			// streaming, before the tool call or any assistant commit is possible.
			select {
			case <-continueResponse:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, "data: "+`{"id":"first","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"mcp-fixture-call","type":"function","function":{"name":"`+mcpHarnessTool+`","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
		} else {
			fmt.Fprint(w, "data: "+`{"id":"last","choices":[{"index":0,"delta":{"role":"assistant","content":"Final live delta"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	t.Cleanup(allowRest)
	provider := domain.Provider{Name: "MCP streaming fixture", Dialect: "openai", Protocol: "openai-completions", BaseURL: server.URL}
	key := "fixture-model-key"
	if err := f.service.Models.SaveProvider(f.ctx, &provider, &key, nil); err != nil {
		t.Fatal(err)
	}
	profile := domain.ModelProfile{Name: "MCP streaming fixture", ProviderID: provider.ID, ModelID: "fixture", Recommendations: map[string]json.RawMessage{
		"context_window": encode(128000), "max_output_tokens": encode(4096), "streaming": encode(true),
	}}
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	if err := f.service.SelectModel(f.ctx, f.session.ID, profile.ID); err != nil {
		t.Fatal(err)
	}
	return allowRest
}

func TestMCPMountPreservesLiveSubscriptionsAndWatermark(t *testing.T) {
	remote := mcpHarnessFixture(t, []mcpclient.Tool{mcpHarnessDefinition("lookup", `{"type":"object"}`)}, nil)
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Fixture response") })
	allowRest := attachMCPStreamModel(t, f)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	feedA, err := f.service.SubscribeSession(ctx, f.session.ID, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer feedA.Close()
	feedB, err := f.service.SubscribeSession(ctx, f.session.ID, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer feedB.Close()
	before, err := f.service.SubmitACP(ctx, f.session.ID, InputRequest{ID: "before-mount", Text: "Before mounting the tool"}, "Editor")
	if err != nil {
		t.Fatal(err)
	}
	watermarkA := readMCPRun(t, ctx, feedA, before.RunID, 0, false, nil)
	watermarkB := readMCPRun(t, ctx, feedB, before.RunID, 0, false, nil)
	if watermarkA == 0 || watermarkA != watermarkB {
		t.Fatalf("subscribers disagree before reopen: %d != %d", watermarkA, watermarkB)
	}
	r, err := f.service.open(ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	previous, previousGeneration := r.core.Load(), r.coreGeneration.Load()
	if err := f.service.MountACPTools(ctx, f.session.ID, "mounting-connection", []mcpclient.ServerSpec{remote}); err != nil {
		t.Fatal(err)
	}
	if r.core.Load() == previous || r.coreGeneration.Load() != previousGeneration+1 {
		t.Fatal("first MCP mount did not reopen the persistent Core Session")
	}
	snapshot, err := f.service.Snapshot(ctx, f.session.ID)
	if err != nil || snapshot.EventSequence < watermarkA {
		t.Fatalf("snapshot lost its event watermark after reopen: %d < %d, %v", snapshot.EventSequence, watermarkA, err)
	}
	after, err := f.service.SubmitACP(ctx, f.session.ID, InputRequest{ID: "after-mount", Text: "Use the mounted tool now"}, "Editor")
	if err != nil {
		t.Fatal(err)
	}
	watermarkA = readMCPRun(t, ctx, feedA, after.RunID, watermarkA, true, allowRest)
	watermarkB = readMCPRun(t, ctx, feedB, after.RunID, watermarkB, true, nil)
	if watermarkA != watermarkB {
		t.Fatalf("subscribers disagreed after reopen: %d != %d", watermarkA, watermarkB)
	}
}

func readMCPRun(t *testing.T, ctx context.Context, subscription *SessionSubscription, runID string, watermark uint64, wantTool bool, onFirstDelta func()) uint64 {
	t.Helper()
	if runID == "" {
		t.Fatal("work did not report its Run ID")
	}
	var started, input, first, tool, final bool
	for {
		select {
		case envelope, ok := <-subscription.Events:
			if !ok {
				t.Fatal("subscriber disconnected at the Core reopen")
			}
			event := envelope.Core
			if event == nil {
				continue
			}
			if event.Sequence <= watermark {
				t.Fatalf("event sequence regressed: %d <= %d", event.Sequence, watermark)
			}
			watermark = event.Sequence
			if event.RunID != runID {
				continue
			}
			if entry := envelope.Entry; entry != nil && entry.Kind == core.EntryMessage && entry.Message != nil && entry.Message.Role == core.RoleUser {
				input = true
			}
			switch event.Type {
			case core.EventRunStart:
				started = true
			case core.EventMessageUpdate:
				if event.Model != nil && event.Model.Type == "text_delta" {
					first = first || event.Model.Text == "First live delta"
					if first && onFirstDelta != nil {
						onFirstDelta()
						onFirstDelta = nil
					}
					final = final || event.Model.Text == "Final live delta"
				}
			case core.EventToolEnd:
				tool = tool || event.ToolCallID == "mcp-fixture-call"
			case core.EventRunEnd:
				if event.Status != "completed" || !started || !input || !final || wantTool && (!first || !tool) {
					t.Fatalf("incomplete stream: run=%s error=%q start=%v input=%v first=%v tool=%v final=%v", event.Status, event.Error, started, input, first, tool, final)
				}
				return watermark
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestMCPSameFingerprintReconnectKeepsCoreAndRejectsOtherMountOwners(t *testing.T) {
	const canonical = `{"type":"object","properties":{"q":{"type":"string"}}}`
	const reordered = `{ "properties": {"q":{"type":"string"}}, "type": "object" }`
	first := mcpHarnessFixture(t, []mcpclient.Tool{mcpHarnessDefinition("zebra", canonical), mcpHarnessDefinition("lookup", canonical)}, nil)
	second := mcpHarnessFixture(t, []mcpclient.Tool{mcpHarnessDefinition("lookup", reordered), mcpHarnessDefinition("zebra", reordered)},
		func(w http.ResponseWriter, _ *http.Request, id json.RawMessage) {
			mcpHarnessJSON(w, id, mcpclient.Result{Content: []mcpclient.Content{{Type: "text", Text: "New connection"}}})
		})
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Fixture response") })
	id := f.session.ID
	if err := f.service.MountACPTools(f.ctx, id, "old", []mcpclient.ServerSpec{first}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.MountACPTools(f.ctx, id, "new", []mcpclient.ServerSpec{second}); !mcpDomainCode(err, "conflict") {
		t.Fatalf("another connection replaced an active mount: %v", err)
	}
	if err := f.service.MountACPTools(f.ctx, id, "new", nil); err != nil {
		t.Fatalf("passive observer was rejected: %v", err)
	}
	r, err := f.service.open(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	oldCore, oldGeneration := r.core.Load(), r.coreGeneration.Load()
	oldState := oldCore.State()
	oldConfiguration := oldCore.Configuration()
	f.service.ReleaseACPMounts("old")
	if _, err := f.service.CallMCPTool(f.ctx, id, mcpHarnessTool, nil); !mcpDomainCode(err, "offline") {
		t.Fatalf("detached connection can still call tools or returned the wrong state: %v", err)
	}
	if tools, err := f.service.MCPTools(f.ctx, id); err != nil || len(tools) != 2 {
		t.Fatalf("grace window lost tool declarations: %+v %v", tools, err)
	}
	if err := f.service.MountACPTools(f.ctx, id, "new", []mcpclient.ServerSpec{second}); err != nil {
		t.Fatal(err)
	}
	f.service.ReleaseACPMounts("old")
	state := r.core.Load().State()
	if r.core.Load() != oldCore || r.coreGeneration.Load() != oldGeneration || state.Version != oldState.Version || state.Branch.ConfigurationID != oldState.Branch.ConfigurationID || !reflect.DeepEqual(r.core.Load().Configuration(), oldConfiguration) {
		t.Fatal("equivalent tool catalogs changed the Core instance or durable configuration")
	}
	result, err := f.service.CallMCPTool(f.ctx, id, mcpHarnessTool, nil)
	if err != nil || len(result.Content) != 1 || result.Content[0].Text != "New connection" {
		t.Fatalf("tool was not routed to the replacement lease: %+v %v", result, err)
	}
	for _, entry := range subagentTestEntries(t, f, id) {
		if entry.Kind == core.EntryConfiguration {
			data := string(encode(entry))
			if strings.Contains(data, first.URL) || strings.Contains(data, second.URL) || strings.Contains(data, first.Headers[0].Value) {
				t.Fatal("Core configuration retained MCP connection information")
			}
		}
	}
	if err := f.service.MountACPTools(f.ctx, id, "new", nil); err != nil {
		t.Fatal(err)
	}
	if r.core.Load() == oldCore || r.coreGeneration.Load() != oldGeneration+1 {
		t.Fatal("explicit removal did not apply a tool-set change")
	}
}

func TestMCPReplacementWaitsUntilCurrentRunFinishes(t *testing.T) {
	const replacementTool = "mcp__fixture__replacement"
	started := make(chan struct{})
	finish := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(finish) })
	defer unblock()
	var oldCalls, newCalls atomic.Int32
	old := mcpHarnessFixture(t, []mcpclient.Tool{mcpHarnessDefinition("lookup", `{"type":"object"}`)},
		func(w http.ResponseWriter, r *http.Request, id json.RawMessage) {
			if oldCalls.Add(1) == 1 {
				close(started)
				select {
				case <-finish:
				case <-r.Context().Done():
					t.Error("replacement cancelled the previous Run's MCP call")
					return
				}
			}
			mcpHarnessJSON(w, id, mcpclient.Result{Content: []mcpclient.Content{{Type: "text", Text: "Old tool completed"}}})
		})
	next := mcpHarnessFixture(t, []mcpclient.Tool{mcpHarnessDefinition("replacement", `{"type":"object"}`)},
		func(w http.ResponseWriter, _ *http.Request, id json.RawMessage) {
			newCalls.Add(1)
			mcpHarnessJSON(w, id, mcpclient.Result{Content: []mcpclient.Content{{Type: "text", Text: "New tool completed"}}})
		})
	f := newFixture(t, 0, func(_ context.Context, index int, _ map[string]any) any {
		if index == 1 {
			return toolReply("blocked-mcp-call", mcpHarnessTool, struct{}{})
		}
		return textReply("Fixture completed.")
	})
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	id := f.session.ID
	if err := f.service.MountACPTools(ctx, id, "owner", []mcpclient.ServerSpec{old}); err != nil {
		t.Fatal(err)
	}
	r, err := f.service.open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	current, generation := r.core.Load(), r.coreGeneration.Load()
	if _, err := f.service.Submit(ctx, id, InputRequest{ID: "busy-mcp-replacement", Text: "Call the old tool"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("the old tool did not start")
	}
	if err := f.service.MountACPTools(ctx, id, "owner", []mcpclient.ServerSpec{next}); err != nil {
		t.Fatal(err)
	}
	if r.core.Load() != current || r.coreGeneration.Load() != generation {
		t.Fatal("mount replacement changed the Core during an active Run")
	}
	if tools, err := f.service.MCPTools(ctx, id); err != nil || len(tools) != 1 || tools[0].Name != mcpHarnessTool {
		t.Fatalf("root exposed replacement tools during the active Run: %+v, %v", tools, err)
	}
	if _, err := f.service.CallMCPTool(ctx, id, mcpHarnessTool, nil); err != nil {
		t.Fatalf("old tools stopped accepting calls before the Run ended: %v", err)
	}
	if _, err := f.service.CallMCPTool(ctx, id, replacementTool, nil); err == nil || newCalls.Load() != 0 {
		t.Fatal("replacement tool was callable before the Run ended")
	}
	child := subagentTestSpawn(t, f, r, "mcp-busy-child", "Inspect the current tool set", false)
	subagentTestFinished(t, f, child.SessionID)
	if tools := subagentTestRequest(t, f, child.SessionID).Tools; !bytes.Contains(encode(tools), []byte(mcpHarnessTool)) || bytes.Contains(encode(tools), []byte(replacementTool)) {
		t.Fatal("delegated model request used the pending tool set")
	}
	unblock()
	eventually(t, ctx, func() bool {
		tools, err := f.service.MCPTools(ctx, id)
		return err == nil && r.coreGeneration.Load() == generation+1 && len(tools) == 1 && tools[0].Name == replacementTool
	})
	for _, entry := range subagentTestEntries(t, f, id) {
		if entry.Kind == core.EntryToolResult && entry.Tool != nil && entry.Tool.Invocation.Call.ID == "blocked-mcp-call" {
			result := entry.Tool.Result
			if result == nil || result.IsError || result.Outcome != core.OutcomeSucceeded || len(result.Content) != 1 || result.Content[0].Text != "Old tool completed" {
				t.Fatalf("active Run did not retain its old tool result: %+v", result)
			}
			response, err := f.service.CallMCPTool(ctx, id, replacementTool, nil)
			if err != nil || len(response.Content) != 1 || response.Content[0].Text != "New tool completed" || oldCalls.Load() != 2 || newCalls.Load() != 1 {
				t.Fatalf("replacement did not take effect exactly at idle: %+v %v, calls old=%d new=%d", response, err, oldCalls.Load(), newCalls.Load())
			}
			return
		}
	}
	t.Fatal("the active Run's old tool result was never committed")
}

func TestMCPDelegatesShareActualRequestToolsAndOtherSessionsAreIsolated(t *testing.T) {
	remote := mcpHarnessFixture(t, []mcpclient.Tool{mcpHarnessDefinition("lookup", `{"type":"object","properties":{"q":{"type":"string"}}}`)}, nil)
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Fixture response") })
	root := subagentTestSeed(t, f)
	if err := f.service.MountACPTools(f.ctx, root.id, "owner", []mcpclient.ServerSpec{remote}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, root.id, InputRequest{ID: "mounted-parent", Text: "Keep the mounted prefix"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, root.id)
	parentRequest := subagentTestRequest(t, f, root.id)
	parentWire := mcpLastWireTools(t, f)
	if !bytes.Contains(parentWire, []byte(mcpHarnessTool)) {
		t.Fatal("mounted tool did not appear in the root's real model request")
	}
	for _, inherited := range []bool{true, false} {
		id := fmt.Sprintf("mcp-child-%v", inherited)
		child := subagentTestSpawn(t, f, root, id, "Check the same tool prefix", inherited)
		subagentTestFinished(t, f, child.SessionID)
		request := subagentTestRequest(t, f, child.SessionID)
		if !reflect.DeepEqual(request.Tools, parentRequest.Tools) || !bytes.Equal(mcpLastWireTools(t, f), parentWire) {
			t.Fatalf("delegated request changed the actual tool declaration prefix (inherit=%t)", inherited)
		}
		result, err := f.service.CallMCPTool(f.ctx, child.SessionID, mcpHarnessTool, nil)
		if err != nil || len(result.Content) != 1 || result.Content[0].Text != "MCP fixture response" {
			t.Fatalf("delegated tool result: %+v %v", result, err)
		}
	}
	other, err := f.service.CreateSession(f.ctx, f.agent.ID, "Separate root")
	if err != nil {
		t.Fatal(err)
	}
	if tools, err := f.service.MCPTools(f.ctx, other.ID); err != nil || len(tools) != 0 {
		t.Fatalf("another root saw the mount: %+v %v", tools, err)
	}
	if _, err := f.service.CallMCPTool(f.ctx, other.ID, mcpHarnessTool, nil); err == nil {
		t.Fatal("another root used the mount")
	}
	if _, err := f.service.Submit(f.ctx, other.ID, InputRequest{ID: "separate-root", Text: "Check isolation"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, other.ID)
	if bytes.Contains(mcpLastWireTools(t, f), []byte(mcpHarnessTool)) {
		t.Fatal("the same Bot's other root model request inherited mounted tools")
	}
}

func mcpLastWireTools(t *testing.T, f *fixture) json.RawMessage {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var body struct{ Tools json.RawMessage }
	if len(f.requests) == 0 || json.Unmarshal(f.requests[len(f.requests)-1], &body) != nil || len(body.Tools) == 0 {
		t.Fatal("fixture captured no model request tools")
	}
	return body.Tools
}

func mcpDomainCode(err error, code string) bool {
	var candidate *domain.Error
	return errors.As(err, &candidate) && string(candidate.Code) == code
}

func TestMCPReleaseCancelsInFlightCall(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	remote := mcpHarnessFixture(t, []mcpclient.Tool{mcpHarnessDefinition("lookup", `{"type":"object"}`)},
		func(w http.ResponseWriter, r *http.Request, _ json.RawMessage) {
			if calls.Add(1) == 1 {
				close(started)
				<-r.Context().Done()
				close(cancelled)
			}
		})
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Fixture response") })
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	if err := f.service.MountACPTools(ctx, f.session.ID, "owner", []mcpclient.ServerSpec{remote}); err != nil {
		t.Fatal(err)
	}
	called := make(chan error, 1)
	go func() {
		_, err := f.service.CallMCPTool(ctx, f.session.ID, mcpHarnessTool, nil)
		called <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("remote tool call did not start")
	}
	f.service.ReleaseACPMounts("owner")
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("revocation did not cancel the real remote request")
	}
	select {
	case err := <-called:
		if err == nil {
			t.Fatal("revoked call returned success")
		}
	case <-ctx.Done():
		t.Fatal("revoked call did not return")
	}
	if _, err := f.service.CallMCPTool(ctx, f.session.ID, mcpHarnessTool, nil); !mcpDomainCode(err, "offline") || calls.Load() != 1 {
		t.Fatalf("revoked connection retained permission: calls=%d error=%v", calls.Load(), err)
	}
}

func TestMCPCallErrorsReturnToModelAndKeepDeclarations(t *testing.T) {
	var calls atomic.Int32
	remote := mcpHarnessFixture(t, []mcpclient.Tool{mcpHarnessDefinition("lookup", `{"type":"object"}`)},
		func(w http.ResponseWriter, _ *http.Request, _ json.RawMessage) {
			calls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		})
	f := newFixture(t, 0, func(_ context.Context, _ int, req map[string]any) any {
		messages := req["messages"].([]any)
		if last := messages[len(messages)-1].(map[string]any); last["role"] == "tool" {
			return textReply("I received the tool error and can continue.")
		}
		return toolReply(domain.ID("mcp-fixture-call"), mcpHarnessTool, struct{}{})
	})
	id := f.session.ID
	if err := f.service.MountACPTools(f.ctx, id, "owner", []mcpclient.ServerSpec{remote}); err != nil {
		t.Fatal(err)
	}
	r, err := f.service.open(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	current, generation := r.core.Load(), r.coreGeneration.Load()
	for _, phase := range []struct {
		name, code string
		outcome    core.Outcome
	}{
		{"remote-error", "mcp_call_failed", core.OutcomeFailed},
		{"offline-grace", "mcp_unavailable", core.OutcomeNotExecuted},
	} {
		if phase.name == "offline-grace" {
			f.service.ReleaseACPMounts("owner")
		}
		if _, err := f.service.Submit(f.ctx, id, InputRequest{ID: phase.name, Text: "Try the mounted MCP tool"}); err != nil {
			t.Fatal(err)
		}
		view := f.idle(t, id)
		if view.LastRun == nil || view.LastRun.Status != "completed" || len(view.Unresolved) != 0 || r.core.Load() != current || r.coreGeneration.Load() != generation {
			t.Fatalf("%s disrupted the Session: %+v", phase.name, view)
		}
		entries := subagentTestEntries(t, f, id)
		correct := false
		for _, entry := range entries {
			if entry.RunID == view.LastRun.ID && entry.Kind == core.EntryToolResult && entry.Tool != nil && entry.Tool.Result != nil {
				result := entry.Tool.Result
				correct = result.IsError && result.Code == phase.code && result.Outcome == phase.outcome && !entry.Tool.NeedsResolution
				break
			}
		}
		if !correct {
			t.Fatalf("%s did not persist a model-visible tool error", phase.name)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("offline grace contacted the remote server: calls=%d", calls.Load())
	}
}

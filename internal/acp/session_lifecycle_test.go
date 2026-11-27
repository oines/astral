package acp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestACPResumeSkipsHistoryAndFollowsLiveTurns(t *testing.T) {
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if request["tools"] != nil {
			return testkit.Text("answer to the prompt")
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
	owner.send(t, 3, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": []contentBlock{{Type: "text", Text: "old prompt"}}})
	if reply, _ := owner.response(t, 3); reply.Error != nil || reply.Result.StopReason != "end_turn" {
		t.Fatalf("first prompt: %+v", reply)
	}

	peer, _, err := dial(t, server.URL, token.Token)
	if err != nil {
		t.Fatal(err)
	}
	peer.initialize(t)
	peer.send(t, 2, "session/resume", map[string]any{"sessionId": sessionID, "cwd": "/work", "mcpServers": []any{}})
	if reply, updates := peer.response(t, 2); reply.Error != nil || len(updates) != 0 {
		t.Fatalf("resume reply = %+v, updates = %+v", reply, updates)
	}
	owner.send(t, 4, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": []contentBlock{{Type: "text", Text: "new prompt"}}})
	var updates []wireMessage
	for updateCount(updates, "agent_message_chunk", "answer to the prompt", "acp") == 0 {
		updates = append(updates, peer.read(t))
	}
	if updateCount(updates, "user_message_chunk", "old prompt", "acp") != 0 || updateCount(updates, "user_message_chunk", "new prompt", "acp") != 1 {
		t.Fatalf("resumed session streamed the wrong messages: %+v", updates)
	}
	if reply, _ := owner.response(t, 4); reply.Error != nil || reply.Result.StopReason != "end_turn" {
		t.Fatalf("second prompt: %+v", reply)
	}

	var record domain.Session
	if err = p.Harness.Store.Get(p.Context, "sessions", sessionID, &record); err != nil {
		t.Fatal(err)
	}
	record.CWD = "/existing"
	if err = p.Harness.Store.Save(p.Context, "sessions", &record.Meta, record); err != nil {
		t.Fatal(err)
	}
	for i, method := range []string{"session/resume", "session/load"} {
		id := i + 3
		peer.send(t, id, method, map[string]any{"sessionId": sessionID, "cwd": "/different", "mcpServers": []any{}})
		if reply, _ := peer.response(t, id); reply.Error == nil || reply.Error.Code != -32602 {
			t.Fatalf("%s with a different working directory returned %+v", method, reply)
		}
	}
}

func TestACPCloseCancelsOnlyItsPromptAndMount(t *testing.T) {
	const hold = "work until the session is closed"
	started := make(chan struct{}, 1)
	var entered atomic.Bool
	fixture := newMCPEcho(t)
	p := testkit.New(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if request["tools"] != nil && strings.Contains(string(testkit.JSON(request["messages"])), hold) && entered.CompareAndSwap(false, true) {
			started <- struct{}{}
			<-ctx.Done()
			return nil
		}
		return testkit.Text("answer")
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
	mount := func(id int, name string) string {
		owner.send(t, id, "session/new", map[string]any{"cwd": "/work", "mcpServers": fixture.spec(name)})
		reply, _ := owner.response(t, id)
		if reply.Error != nil || reply.Result.SessionID == "" {
			t.Fatalf("create session with %s mount: %+v", name, reply)
		}
		return reply.Result.SessionID
	}
	a, b := mount(2, "first"), mount(3, "second")
	peer, _, err := dial(t, server.URL, token.Token)
	if err != nil {
		t.Fatal(err)
	}
	peer.initialize(t)
	resume := func(id int) {
		peer.send(t, id, "session/resume", map[string]any{"sessionId": a, "cwd": "/work", "mcpServers": []any{}})
		if reply, _ := peer.response(t, id); reply.Error != nil {
			t.Fatalf("peer resume: %+v", reply)
		}
	}
	resume(2)
	owner.send(t, 4, "session/prompt", map[string]any{"sessionId": a, "prompt": []contentBlock{{Type: "text", Text: hold}}})
	select {
	case <-started:
	case <-owner.ctx.Done():
		t.Fatal(owner.ctx.Err())
	}
	peer.send(t, 3, "session/close", map[string]string{"sessionId": a})
	if reply, _ := peer.response(t, 3); reply.Error == nil || reply.Error.Code != rpcConflict {
		t.Fatalf("observer close while another attachment has work: %+v", reply)
	}
	if snapshot, err := p.Harness.Snapshot(p.Context, a); err != nil || !snapshot.Running {
		t.Fatalf("observer close stopped the owner's prompt: running=%v err=%v", snapshot.Running, err)
	}
	call := func(sessionID, name string) error {
		_, err := p.Harness.CallMCPTool(p.Context, sessionID, "mcp__"+name+"__echo", json.RawMessage(`{"value":"example"}`))
		return err
	}
	if err := call(a, "first"); err != nil {
		t.Fatalf("observer close affected owner's mount: %v", err)
	}
	owner.send(t, 5, "session/close", map[string]string{"sessionId": a})
	owner.send(t, 6, "session/prompt", map[string]any{"sessionId": a, "prompt": []contentBlock{{Type: "text", Text: "raced the close"}}})
	seen := make(map[int]bool)
	for len(seen) < 3 {
		reply := owner.read(t)
		switch reply.ID {
		case 0:
			continue
		case 4:
			if reply.Error != nil || reply.Result.StopReason != "cancelled" {
				t.Fatalf("original prompt did not finish cancelled: %+v", reply)
			}
		case 5:
			if reply.Error != nil || !seen[4] {
				t.Fatalf("close did not complete after the original prompt: %+v", reply)
			}
		case 6:
			if reply.Error == nil || (reply.Error.Code != rpcConflict && reply.Error.Code != -32002) {
				t.Fatalf("prompt sent after close was admitted: %+v", reply)
			}
		default:
			t.Fatalf("unexpected response: %+v", reply)
		}
		seen[reply.ID] = true
	}
	if err := call(a, "first"); err == nil {
		t.Fatal("closed session still has a callable MCP mount")
	}
	if err := call(b, "second"); err != nil {
		t.Fatalf("owner close affected the other session's mount: %v", err)
	}
	owner.send(t, 7, "session/close", map[string]string{"sessionId": a})
	if reply, _ := owner.response(t, 7); reply.Error != nil {
		t.Fatalf("repeated close: %+v", reply)
	}
	owner.send(t, 8, "session/prompt", map[string]any{"sessionId": a, "prompt": []contentBlock{{Type: "text", Text: "closed attachment"}}})
	if reply, _ := owner.response(t, 8); reply.Error == nil || reply.Error.Code != -32002 {
		t.Fatalf("closed attachment is still promptable: %+v", reply)
	}
	peer.send(t, 5, "session/prompt", map[string]any{"sessionId": a, "prompt": []contentBlock{{Type: "text", Text: "still attached"}}})
	if reply, _ := peer.response(t, 5); reply.Error != nil || reply.Result.StopReason != "end_turn" {
		t.Fatalf("owner close detached the observer: %+v", reply)
	}
}

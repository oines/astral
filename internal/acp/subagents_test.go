package acp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/testkit"
)

func TestACPChildLifecycleAndHistory(t *testing.T) {
	const (
		childTitle = "Fixture researcher"
		childOnly  = "Private child transcript fixture"
	)
	release := make(chan struct{})
	releaseWork := sync.OnceFunc(func() { close(release) })
	defer releaseWork()
	var spawned atomic.Bool
	p := testkit.New(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		if request["tools"] == nil {
			return testkit.Text("Conversation")
		}
		if strings.Contains(string(testkit.JSON(request["messages"])), "You are a subagent of Session") {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-release:
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(testkit.JSON(testkit.Text(childOnly)))
				}
			})
		}
		if spawned.CompareAndSwap(false, true) {
			return testkit.Tool("spawn_agent", map[string]string{"name": childTitle, "task": "Check fixture evidence"})
		}
		return testkit.Text("Parent response")
	})
	service := New(p.Harness)
	server := httptest.NewServer(service)
	t.Cleanup(server.Close)
	token, err := service.CreateToken(p.Context, p.Agent.ID, "Editor")
	if err != nil {
		t.Fatal(err)
	}
	connect := func(capability bool) testClient {
		t.Helper()
		client, _, err := dial(t, server.URL, token.Token)
		if err != nil {
			t.Fatal(err)
		}
		caps := map[string]any{}
		if capability {
			caps["subagents"] = map[string]any{}
		}
		client.send(t, 1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": caps})
		if response, _ := client.response(t, 1); response.Error != nil {
			t.Fatalf("initialize: %+v", response.Error)
		}
		return client
	}
	type lifecycleMessage struct {
		ID     int       `json:"id"`
		Method string    `json:"method"`
		Error  *rpcError `json:"error"`
		Params struct {
			Parent string         `json:"sessionId"`
			Update subagentUpdate `json:"update"`
		} `json:"params"`
	}
	read := func(client testClient) lifecycleMessage {
		t.Helper()
		var msg lifecycleMessage
		if err := wsjson.Read(client.ctx, client.conn, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Method == "session/update" && msg.Params.Parent != p.Session.ID {
			t.Fatalf("unexpected child transcript or misattributed update: %+v", msg)
		}
		return msg
	}
	setup := func(client testClient, method string) []subagentUpdate {
		t.Helper()
		client.send(t, 2, method, map[string]any{"sessionId": p.Session.ID, "cwd": p.Session.CWD, "mcpServers": []any{}})
		var updates []subagentUpdate
		for {
			msg := read(client)
			if msg.ID == 2 {
				if msg.Error != nil {
					t.Fatalf("%s: %+v", method, msg.Error)
				}
				return updates
			}
			if msg.Params.Update.Kind == "subagent_update" {
				updates = append(updates, msg.Params.Update)
			}
		}
	}
	nextChild := func(client testClient) subagentUpdate {
		t.Helper()
		for {
			msg := read(client)
			if msg.Params.Update.Kind == "subagent_update" {
				return msg.Params.Update
			}
		}
	}
	first := connect(true)
	if updates := setup(first, "session/load"); len(updates) != 0 {
		t.Fatalf("empty parent replayed children: %+v", updates)
	}
	first.send(t, 3, "session/prompt", map[string]any{"sessionId": p.Session.ID, "prompt": []map[string]string{{"type": "text", "text": "Delegate the fixture check."}}})
	child := nextChild(first)
	if child.SessionID == "" || child.SessionID == p.Session.ID || child.Title != childTitle || child.CWD != p.Session.CWD || child.State == nil || child.State.State != "running" {
		t.Fatalf("incorrect live child start: %+v", child)
	}
	if active, err := p.Harness.ACPHasActiveSubagents(p.Context, p.Session.ID); err != nil || !active {
		t.Fatalf("scope did not observe the running child: active=%t err=%v", active, err)
	}
	first.send(t, 4, "session/close", map[string]string{"sessionId": p.Session.ID})
	for {
		msg := read(first)
		if msg.ID == 4 {
			if msg.Error == nil || msg.Error.Code != rpcConflict {
				t.Fatalf("session with active child closed: %+v", msg)
			}
			break
		}
	}
	releaseWork()
	completed := nextChild(first)
	if completed.SessionID != child.SessionID || completed.State == nil || completed.State.State != "idle" {
		t.Fatalf("incorrect live child completion: %+v", completed)
	}
	if active, err := p.Harness.ACPHasActiveSubagents(p.Context, p.Session.ID); err != nil || active {
		t.Fatalf("scope still reports work after child completion: active=%t err=%v", active, err)
	}

	reloader := connect(true)
	replay := setup(reloader, "session/load")
	var states []string
	for _, update := range replay {
		if update.SessionID != child.SessionID || update.CWD != "" || update.State == nil {
			t.Fatalf("incorrect replay association: %+v", update)
		}
		states = append(states, update.State.State)
	}
	if !slices.Equal(states, []string{"running", "idle"}) {
		t.Fatalf("historical child states, in order before load reply = %v", states)
	}
	if got := nextChild(reloader); got.SessionID != child.SessionID || got.State == nil || got.State.State != "idle" {
		t.Fatalf("load did not report current child state after the response: %+v", got)
	}
	resumer := connect(true)
	if updates := setup(resumer, "session/resume"); len(updates) != 0 {
		t.Fatalf("resume replayed historical child changes: %+v", updates)
	}
	if got := nextChild(resumer); got.SessionID != child.SessionID || got.State == nil || got.State.State != "idle" {
		t.Fatalf("resume did not synchronize the current child: %+v", got)
	}
	legacy := connect(false)
	if updates := setup(legacy, "session/load"); len(updates) != 0 {
		t.Fatalf("client without subagent capability got child updates: %+v", updates)
	}
	resumer.send(t, 4, "session/load", map[string]any{"sessionId": child.SessionID, "cwd": p.Session.CWD, "mcpServers": []any{}})
	if response, _ := resumer.response(t, 4); response.Error == nil {
		t.Fatal("announcing a child allowed loading its conversation directly")
	}
}

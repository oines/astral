package acp

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

func TestACPListSessionsPagesCurrentBotDirectory(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("listing sessions should not query the model")
		return testkit.Text("unexpected")
	})
	otherBotID := domain.ID("other_bot")
	id := func(i int) string { return fmt.Sprintf("visible-%03d", i) }
	if err := p.Store.Update(p.Context, func(tx *persistence.Tx) error {
		bot := domain.Agent{Meta: domain.Meta{ID: otherBotID}, Name: "Other Bot", DefaultModelID: p.Profile.ID}
		if err := tx.Save(p.Context, "agents", &bot.Meta, &bot); err != nil {
			return err
		}
		records := []domain.Session{
			{Meta: domain.Meta{ID: "foreign"}, AgentID: otherBotID, CWD: "/work"},
			{Meta: domain.Meta{ID: "private"}, AgentID: p.Agent.ID, CWD: "/work", Private: true},
			{Meta: domain.Meta{ID: "archived"}, AgentID: p.Agent.ID, CWD: "/work", Archived: true},
			{Meta: domain.Meta{ID: "deleting"}, AgentID: p.Agent.ID, CWD: "/work", Deletion: &domain.SessionDeletion{RolloutID: "rollout"}},
			{Meta: domain.Meta{ID: "subagent"}, AgentID: p.Agent.ID, CWD: "/work", SubagentParentID: id(0)},
			{Meta: domain.Meta{ID: "relative-cwd"}, AgentID: p.Agent.ID, CWD: "work"},
			{Meta: domain.Meta{ID: "empty-cwd"}, AgentID: p.Agent.ID},
			{Meta: domain.Meta{ID: "other-cwd"}, AgentID: p.Agent.ID, CWD: `C:\work`},
		}
		for i := 0; i < acpSessionListPageSize+2; i++ {
			records = append(records, domain.Session{
				Meta: domain.Meta{ID: id(i)}, AgentID: p.Agent.ID, CWD: "/work",
				Title: "Workspace session",
			})
		}
		for i := range records {
			record := &records[i]
			if err := tx.Save(p.Context, "sessions", &record.Meta, record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := New(p.Harness)
	server := httptest.NewServer(svc)
	defer server.Close()
	connect := func(botID string) testClient {
		t.Helper()
		token, err := svc.CreateToken(p.Context, botID, "List client")
		if err != nil {
			t.Fatal(err)
		}
		client, _, err := dial(t, server.URL, token.Token)
		if err != nil {
			t.Fatal(err)
		}
		client.send(t, 1, "initialize", map[string]int{"protocolVersion": 1})
		var reply struct {
			Result struct {
				Capabilities struct {
					Session map[string]any `json:"sessionCapabilities"`
				} `json:"agentCapabilities"`
			} `json:"result"`
		}
		if err := wsjson.Read(client.ctx, client.conn, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Result.Capabilities.Session["list"] == nil {
			t.Fatalf("initialize did not advertise session/list: %+v", reply)
		}
		return client
	}
	client := connect(p.Agent.ID)
	list := func(client testClient, params any) (acpSessionListResult, *rpcError) {
		t.Helper()
		message := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/list"}
		if params != nil {
			message["params"] = params
		}
		if err := wsjson.Write(client.ctx, client.conn, message); err != nil {
			t.Fatal(err)
		}
		var reply struct {
			ID     int                  `json:"id"`
			Result acpSessionListResult `json:"result"`
			Error  *rpcError            `json:"error"`
		}
		if err := wsjson.Read(client.ctx, client.conn, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.ID != 2 {
			t.Fatalf("unexpected list reply: %+v", reply)
		}
		return reply.Result, reply.Error
	}
	first, rpcErr := list(client, map[string]string{"cwd": "/work"})
	if rpcErr != nil || len(first.Sessions) != acpSessionListPageSize || first.NextCursor == "" {
		t.Fatalf("first page has %d sessions, cursor %q, error %+v", len(first.Sessions), first.NextCursor, rpcErr)
	}
	for i, info := range first.Sessions {
		if info.SessionID != id(i) || info.CWD != "/work" || info.Title != "Workspace session" {
			t.Fatalf("first page item %d: %+v", i, info)
		}
	}

	// A cursor is a position, and does not keep a record visible across
	// requests or depend on the anchor record still existing.
	if err := p.Store.Update(p.Context, func(tx *persistence.Tx) error {
		var record domain.Session
		anchorID := id(acpSessionListPageSize - 1)
		if err := tx.Get(p.Context, "sessions", anchorID, &record); err != nil {
			return err
		}
		if err := tx.Delete(p.Context, "sessions", anchorID, record.Revision); err != nil {
			return err
		}
		record = domain.Session{}
		if err := tx.Get(p.Context, "sessions", id(acpSessionListPageSize), &record); err != nil {
			return err
		}
		record.Private = true
		return tx.Save(p.Context, "sessions", &record.Meta, &record)
	}); err != nil {
		t.Fatal(err)
	}
	second, rpcErr := list(client, map[string]string{"cwd": "/work", "cursor": first.NextCursor})
	if rpcErr != nil || len(second.Sessions) != 1 || second.Sessions[0].SessionID != id(acpSessionListPageSize+1) || second.NextCursor != "" {
		t.Fatalf("second page: %+v, error %+v", second, rpcErr)
	}
	empty, rpcErr := list(client, map[string]string{"cwd": "/absent"})
	if rpcErr != nil || empty.Sessions == nil || len(empty.Sessions) != 0 || empty.NextCursor != "" {
		t.Fatalf("empty page: %+v, error %+v", empty, rpcErr)
	}
	unfiltered, rpcErr := list(client, nil)
	if rpcErr != nil || len(unfiltered.Sessions) != acpSessionListPageSize || unfiltered.Sessions[0].SessionID != "other-cwd" || unfiltered.NextCursor == "" {
		t.Fatalf("unfiltered page has %d sessions, cursor %q, error %+v", len(unfiltered.Sessions), unfiltered.NextCursor, rpcErr)
	}
	last, rpcErr := list(client, map[string]string{"cursor": unfiltered.NextCursor})
	if rpcErr != nil || len(last.Sessions) != 1 || last.Sessions[0].SessionID != id(acpSessionListPageSize+1) || last.NextCursor != "" {
		t.Fatalf("unfiltered last page: %+v, error %+v", last, rpcErr)
	}
	for _, params := range []any{
		map[string]string{"cwd": "work"},
		map[string]string{"cwd": ""},
		map[string]int{"cwd": 5},
		map[string]string{"cursor": ""},
		map[string]string{"cursor": "invalid"},
		map[string]string{"cwd": `C:\work`, "cursor": first.NextCursor},
		map[string]string{"cursor": first.NextCursor},
		[]any{},
	} {
		if _, rpcErr := list(client, params); rpcErr == nil || rpcErr.Code != -32602 {
			t.Fatalf("list params %+v returned %+v", params, rpcErr)
		}
	}
	other := connect(otherBotID)
	if _, rpcErr := list(other, map[string]string{"cwd": "/work", "cursor": first.NextCursor}); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("cursor reused across Bots: %+v", rpcErr)
	}
}

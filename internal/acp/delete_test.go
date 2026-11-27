package acp

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

func TestACPDeleteArchivesVisibleSession(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any {
		return testkit.Text("Preserved answer")
	})
	cwd := p.Daemons[0].Hello.Home
	svc := New(p.Harness)
	server := httptest.NewServer(svc)
	defer server.Close()
	token, err := svc.CreateToken(p.Context, p.Agent.ID, "Archive client")
	if err != nil {
		t.Fatal(err)
	}
	client, _, err := dial(t, server.URL, token.Token)
	if err != nil {
		t.Fatal(err)
	}
	client.initialize(t)
	client.send(t, 2, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	created, _ := client.response(t, 2)
	sessionID := created.Result.SessionID
	if created.Error != nil || sessionID == "" {
		t.Fatalf("session/new: %+v", created)
	}
	client.send(t, 3, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []contentBlock{{Type: "text", Text: "Preserve my history"}},
	})
	prompt, _ := client.response(t, 3)
	if prompt.Error != nil || prompt.Result.StopReason != "end_turn" {
		t.Fatalf("session/prompt: %+v", prompt)
	}
	state, err := p.CoreStore.LoadState(p.Context, sessionID)
	if err != nil || state.Branch.LastMessageID == "" {
		t.Fatalf("load prompt state: %+v, %v", state.Branch, err)
	}

	otherBot := domain.Agent{Meta: domain.Meta{ID: domain.ID("other_bot")}, Name: "Other Bot"}
	protected := []domain.Session{
		{Meta: domain.Meta{ID: "private-session"}, AgentID: p.Agent.ID, CWD: cwd, Private: true},
		{Meta: domain.Meta{ID: "child-session"}, AgentID: p.Agent.ID, CWD: cwd, SubagentParentID: p.Session.ID},
		{Meta: domain.Meta{ID: "foreign-session"}, AgentID: otherBot.ID, CWD: cwd},
		{Meta: domain.Meta{ID: "bound-session"}, AgentID: p.Agent.ID, CWD: cwd, BindingID: "conversation-binding"},
	}
	if err := p.Store.Update(p.Context, func(tx *persistence.Tx) error {
		if err := tx.Save(p.Context, "agents", &otherBot.Meta, &otherBot); err != nil {
			return err
		}
		for i := range protected {
			record := &protected[i]
			if err := tx.Save(p.Context, "sessions", &record.Meta, record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	listContains := func(rpcID int, sessionID string) bool {
		t.Helper()
		client.send(t, rpcID, "session/list", map[string]string{"cwd": cwd})
		for {
			var reply struct {
				ID     int                  `json:"id"`
				Result acpSessionListResult `json:"result"`
				Error  *rpcError            `json:"error"`
			}
			if err := wsjson.Read(client.ctx, client.conn, &reply); err != nil {
				t.Fatal(err)
			}
			if reply.ID == 0 {
				continue
			}
			if reply.ID != rpcID || reply.Error != nil {
				t.Fatalf("session/list: %+v", reply)
			}
			for _, info := range reply.Result.Sessions {
				if info.SessionID == sessionID {
					return true
				}
			}
			return false
		}
	}
	if !listContains(4, sessionID) {
		t.Fatal("new session missing from the directory")
	}

	deleteSession := func(rpcID int, id string) *rpcError {
		t.Helper()
		client.send(t, rpcID, "session/delete", map[string]string{"sessionId": id})
		response, _ := client.response(t, rpcID)
		return response.Error
	}
	if rpcErr := deleteSession(5, sessionID); rpcErr != nil {
		t.Fatalf("archive visible session: %+v", rpcErr)
	}
	var archived domain.Session
	if err := p.Store.Get(p.Context, "sessions", sessionID, &archived); err != nil || !archived.Archived || archived.Deletion != nil {
		t.Fatalf("session was not archived: %+v, %v", archived, err)
	}
	history, err := p.CoreStore.Get(p.Context, state.RolloutID, state.Branch.LastMessageID)
	if err != nil || history.Entry.Message == nil {
		t.Fatalf("archive lost session history: %v", err)
	}
	if listContains(6, sessionID) {
		t.Fatal("archived session remains in the directory")
	}
	for i, id := range []string{sessionID, "never-existed", "private-session", "child-session", "foreign-session"} {
		if rpcErr := deleteSession(7+i, id); rpcErr != nil {
			t.Fatalf("idempotent deletion for %q: %+v", id, rpcErr)
		}
	}
	if rpcErr := deleteSession(12, "bound-session"); rpcErr == nil || rpcErr.Code != rpcConflict {
		t.Fatalf("deletion of a bound session: %+v", rpcErr)
	}
	for _, original := range protected {
		var current domain.Session
		if err := p.Store.Get(p.Context, "sessions", original.ID, &current); err != nil || current.Archived || current.Revision != original.Revision {
			t.Fatalf("deletion changed protected session %q: %+v, %v", original.ID, current, err)
		}
	}
	if rpcErr := deleteSession(13, ""); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("deletion without sessionId: %+v", rpcErr)
	}
}

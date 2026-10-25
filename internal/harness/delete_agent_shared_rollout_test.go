package harness

import (
	"context"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestDeleteAgentRejectsSharedRolloutBeforeIrreversibleIntent(t *testing.T) {
	for _, sameBot := range []bool{true, false} {
		name := "within-Bot"
		if !sameBot {
			name = "across-Bots"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("unused") })
			state, err := f.coreStore.LoadState(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			child := f.session
			child.Meta = domain.Meta{ID: domain.ID("session")}
			if !sameBot {
				other := f.agent
				other.Meta, other.Name = domain.Meta{}, "Other Bot"
				if err := f.service.SaveAgent(f.ctx, &other); err != nil {
					t.Fatal(err)
				}
				child.AgentID = other.ID
			}
			if _, err := f.coreStore.Fork(f.ctx, core.ForkRequest{SourceID: f.session.ID, SourceRolloutID: state.RolloutID, TargetID: child.ID, HeadID: state.Branch.HeadID, Mode: core.ForkShared}); err != nil {
				t.Fatal(err)
			}
			if err := f.store.Save(f.ctx, "sessions", &child.Meta, &child); err != nil {
				t.Fatal(err)
			}
			connection := domain.Connection{Meta: domain.Meta{ID: domain.ID("connection")}, AgentID: f.agent.ID, Enabled: true}
			if err := f.store.Save(f.ctx, "connections", &connection.Meta, &connection); err != nil {
				t.Fatal(err)
			}
			binding := domain.ConversationBinding{Meta: domain.Meta{ID: domain.ID("binding")}, AgentID: f.agent.ID, SessionID: f.session.ID, Enabled: true}
			if err := f.store.Save(f.ctx, "bindings", &binding.Meta, &binding); err != nil {
				t.Fatal(err)
			}
			if err := f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err == nil {
				t.Fatal("shared history admitted irreversible Bot deletion")
			}
			var agent domain.Agent
			if err := f.store.Get(f.ctx, "agents", f.agent.ID, &agent); err != nil || agent.Deletion || agent.Revision != f.agent.Revision {
				t.Fatalf("rejected deletion changed the Bot: %+v %v", agent, err)
			}
			for _, id := range []string{f.session.ID, child.ID} {
				var session domain.Session
				if err := f.store.Get(f.ctx, "sessions", id, &session); err != nil || session.Deletion != nil {
					t.Fatalf("rejected deletion marked Session %s: %+v %v", id, session, err)
				}
				retained, err := f.coreStore.LoadState(f.ctx, id)
				if err != nil || retained.RolloutID != state.RolloutID {
					t.Fatal("shared Core history changed:", id, err)
				}
			}
			var retainedConnection domain.Connection
			if err := f.store.Get(f.ctx, "connections", connection.ID, &retainedConnection); err != nil || !retainedConnection.Enabled || retainedConnection.Revision != connection.Revision {
				t.Fatalf("rejected deletion disabled the account: %+v %v", retainedConnection, err)
			}
			var retainedBinding domain.ConversationBinding
			if err := f.store.Get(f.ctx, "bindings", binding.ID, &retainedBinding); err != nil || !retainedBinding.Enabled || retainedBinding.Revision != binding.Revision {
				t.Fatalf("rejected deletion disabled the binding: %+v %v", retainedBinding, err)
			}
			if err := f.service.ResumeSessionDeletions(f.ctx); err != nil {
				t.Fatal("rejected deletion left an unresumable intent:", err)
			}
			if _, err := f.service.CreateSession(f.ctx, f.agent.ID, "Still available"); err != nil {
				t.Fatal("rejected deletion blocked the Bot:", err)
			}
		})
	}
}

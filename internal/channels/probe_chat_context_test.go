package channels

import (
	"context"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestProbeRejectsChatMetadataOutsideCurrentBinding(t *testing.T) {
	for _, change := range []string{"stale binding", "different session chat", "different account Agent"} {
		t.Run(change, func(t *testing.T) {
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
				return testkit.Tool("decide", map[string]bool{"participate": true})
			})
			s := New(p.Store, p.Harness)
			bot := newBot(t)
			connection := bot.connection(t, s, p.Agent.ID)
			binding := createBinding(t, p, s, connection, "-100", "")
			switch change {
			case "stale binding":
				current := binding
				current.Enabled = false
				if err := p.Store.Save(p.Context, "bindings", &current.Meta, &current); err != nil {
					t.Fatal(err)
				}
			case "different session chat":
				var session domain.Session
				if err := p.Store.Get(p.Context, "sessions", binding.SessionID, &session); err != nil {
					t.Fatal(err)
				}
				session.ConversationID = "another-chat"
				if err := p.Store.Save(p.Context, "sessions", &session.Meta, &session); err != nil {
					t.Fatal(err)
				}
			case "different account Agent":
				connection.AgentID = "another-Agent"
				if err := p.Store.Save(p.Context, "connections", &connection.Meta, &connection); err != nil {
					t.Fatal(err)
				}
			}
			if participate, err := s.shouldParticipate(p.Context, binding, core.Context{}); err == nil || participate {
				t.Fatalf("Probe used mismatched bound-chat metadata: %v %v", participate, err)
			}
			if p.Requests.Load() != 0 {
				t.Fatal("Probe sent a model request with mismatched chat metadata")
			}
		})
	}
}

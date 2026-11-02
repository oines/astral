package persistence

import (
	"context"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestPrivateSubagentAccessWithoutReaderRetainsDirectParentScope(t *testing.T) {
	child := domain.Session{Meta: domain.Meta{ID: "child"}, AgentID: "bot", Private: true, SubagentParentID: "parent"}
	for _, test := range []struct {
		name, agent, caller string
		allowed             bool
	}{
		{"itself", "bot", "child", true},
		{"direct parent", "bot", "parent", true},
		{"ancestor", "bot", "root", false},
		{"sibling", "bot", "sibling", false},
		{"other Bot", "foreign", "parent", false},
		{"empty identity", "bot", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := CheckSessionAccess(WithSessionScope(context.Background(), test.agent, test.caller), child)
			if test.allowed && err != nil || !test.allowed && !domain.IsMissing(err) {
				t.Fatalf("unexpected access: allowed=%v error=%v", test.allowed, err)
			}
		})
	}
	if err := CheckSessionAccess(context.Background(), child); err != nil {
		t.Fatalf("Owner cannot read private child: %v", err)
	}
	// A Fork's historical origin is independent from subagent control rights.
	fork := child
	fork.SubagentParentID, fork.ParentSessionID = "", "parent"
	if err := CheckSessionAccess(WithSessionScope(context.Background(), "bot", "parent"), fork); !domain.IsMissing(err) {
		t.Fatalf("Fork provenance granted parent access: %v", err)
	}
	if err := CheckSessionAccess(WithSessionScope(context.Background(), "bot", ""), fork); !domain.IsMissing(err) {
		t.Fatalf("empty identity bypassed ordinary private scope: %v", err)
	}
	child.Deletion = &domain.SessionDeletion{}
	if err := CheckSessionAccess(WithSessionScope(context.Background(), "bot", "parent"), child); !domain.IsMissing(err) {
		t.Fatalf("direct parent bypassed child deletion: %v", err)
	}
}

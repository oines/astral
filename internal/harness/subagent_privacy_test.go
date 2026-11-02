package harness

import (
	"context"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestRootPrivacyFollowsDelegatedSubtreeButNotOrdinaryForks(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Evidence recorded.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "private-child", "Investigate this evidence.", true)
	subagentTestFinished(t, f, child.SessionID)
	childRuntime, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	grandchild := subagentTestSpawn(t, f, childRuntime, "private-grandchild", "Verify this independent result.", false)
	subagentTestFinished(t, f, grandchild.SessionID)
	ordinary, err := f.service.Fork(f.ctx, parent.id, parent.core.Load().Head(), "Independent user fork", "ordinary-privacy-fork")
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []bool{true, false} {
		if err = f.service.SaveSessionPresentation(f.ctx, parent.id, nil, nil, nil, &private); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{parent.id, child.SessionID, grandchild.SessionID} {
			var record domain.Session
			if err = f.store.Get(f.ctx, "sessions", id, &record); err != nil || record.Private != private {
				t.Fatalf("privacy=%v session=%+v error=%v", private, record, err)
			}
			if private && id != parent.id {
				other := persistence.WithSessionScope(f.ctx, f.agent.ID, ordinary.ID)
				if err = persistence.CheckSessionAccess(other, record); !domain.IsMissing(err) {
					t.Fatalf("unrelated Session could access private descendant: %v", err)
				}
			}
		}
		if err = f.store.Get(f.ctx, "sessions", ordinary.ID, &ordinary); err != nil || ordinary.Private {
			t.Fatalf("ordinary Fork was changed by delegated subtree privacy: %+v %v", ordinary, err)
		}
	}
}

func TestPrivateRootCreatesPrivateSharedAndIndependentSubagents(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Private evidence recorded.") })
	parent := subagentTestSeed(t, f)
	private := true
	if err := f.service.SaveSessionPresentation(f.ctx, parent.id, nil, nil, nil, &private); err != nil {
		t.Fatal(err)
	}
	for _, inherited := range []bool{true, false} {
		execution := "private-new"
		if inherited {
			execution = "private-fork"
		}
		child := subagentTestSpawn(t, f, parent, execution, "Keep this work private.", inherited)
		subagentTestFinished(t, f, child.SessionID)
		var record domain.Session
		if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
			t.Fatal(err)
		}
		if !record.Private {
			t.Fatalf("private root created a public child (inherit_context=%v)", inherited)
		}
		unrelated := persistence.WithSessionScope(f.ctx, f.agent.ID, "unrelated-session")
		if err := persistence.CheckSessionAccess(unrelated, record); !domain.IsMissing(err) {
			t.Fatalf("private child is readable by an unrelated Session: %v", err)
		}
		directParent := persistence.WithSessionScope(f.ctx, f.agent.ID, parent.id)
		if err := persistence.CheckSessionAccess(directParent, record); err != nil {
			t.Fatalf("private child's direct parent lost read access: %v", err)
		}
	}
}

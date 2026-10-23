package harness

import (
	"context"
	"errors"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/recall"
)

func TestForkClonesSelectedHistoryIntoIndependentRollout(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("reply") })
	submit := func(id, text string) View {
		t.Helper()
		if _, err := f.service.Submit(f.ctx, id, InputRequest{ID: domain.ID("input"), Text: text}); err != nil {
			t.Fatal(err)
		}
		return f.idle(t, id)
	}
	boundary := submit(f.session.ID, "copied-boundary")
	parent := submit(f.session.ID, "parent-later")
	childID := domain.ID("session")
	child, err := f.service.Fork(f.ctx, f.session.ID, boundary.HeadID, "copy", childID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.coreStore.LoadState(f.ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RolloutID == parent.RolloutID || state.Creation.ForkMode != core.ForkDetached || child.ParentSessionID != f.session.ID || child.ForkEntryID != boundary.HeadID {
		t.Fatalf("Fork did not create an independent copy: %+v %+v", child, state)
	}
	if child.Source != "web" || child.BindingID != "" || child.ConversationID != "" {
		t.Fatal("Fork retained inbound binding")
	}
	check := func(id, query string, want int) {
		t.Helper()
		page, err := f.service.Recall.Search(f.ctx, f.agent.ID, recall.SearchRequest{Query: query, SessionIDs: []string{id}})
		if err != nil || len(page.Hits) != want {
			t.Fatalf("%s %q: %+v %v", id, query, page, err)
		}
	}
	check(child.ID, "copied-boundary", 1)
	check(child.ID, "parent-later", 0)
	if _, err = f.coreStore.Get(f.ctx, state.RolloutID, parent.HeadID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("copy contains source's later entry: %v", err)
	}
	submit(child.ID, "child-later")
	check(f.session.ID, "child-later", 0)
	submit(f.session.ID, "parent-after-copy")
	check(child.ID, "parent-after-copy", 0)
	beforeRetry, err := f.coreStore.LoadState(f.ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	retry, err := f.service.Fork(f.ctx, f.session.ID, boundary.HeadID, "copy", childID)
	if err != nil || retry.ID != child.ID {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	afterRetry, err := f.coreStore.LoadState(f.ctx, child.ID)
	if err != nil || afterRetry.RolloutID != beforeRetry.RolloutID || afterRetry.Branch.HeadID != beforeRetry.Branch.HeadID {
		t.Fatalf("retry replaced independent history: %+v %v", afterRetry, err)
	}
	check(child.ID, "child-later", 1)

	// A catalogued child whose Core creation did not complete is recovered
	// through initialize, which must use the same detached mode as the button.
	recovered := child
	recovered.Meta = domain.Meta{ID: domain.ID("session")}
	if err = f.store.Save(f.ctx, "sessions", &recovered.Meta, &recovered); err != nil {
		t.Fatal(err)
	}
	if err = f.service.InitializeSession(f.ctx, recovered.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := f.coreStore.LoadState(f.ctx, recovered.ID)
	if err != nil || restored.Creation.ForkMode != core.ForkDetached || restored.RolloutID == state.RolloutID || restored.RolloutID == parent.RolloutID {
		t.Fatalf("recovery created shared rollout: %+v %v", restored, err)
	}
	check(recovered.ID, "copied-boundary", 1)
	check(recovered.ID, "parent-later", 0)
}

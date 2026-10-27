package harness

import (
	"context"
	"errors"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
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

func TestHistoricalCloneKeepsSelectedReasoningAndOpensAfterModelDeletion(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("reply") })
	var original domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &original); err != nil {
		t.Fatal(err)
	}
	original.Recommendations["reasoning"] = encode(map[string]any{"efforts": []string{"low", "high"}})
	if err := f.service.Models.SaveProfile(f.ctx, &original); err != nil {
		t.Fatal(err)
	}
	if err := f.service.SelectModel(f.ctx, f.session.ID, original.ID, &core.ReasoningOptions{Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "historical", Text: "selected historical content"}); err != nil {
		t.Fatal(err)
	}
	boundary := f.idle(t, f.session.ID)
	next := anotherModel(t, f)
	if err := f.service.SelectModel(f.ctx, f.session.ID, next.ID, &core.ReasoningOptions{Effort: "low"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "future", Text: "parent future content"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error {
		return tx.Delete(f.ctx, "models", original.ID, original.Revision)
	}); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	before := f.count()
	child, err := f.service.Fork(f.ctx, f.session.ID, boundary.HeadID, "historical clone", domain.ID("session"))
	if err != nil {
		t.Fatal("deleted historical model blocked clone creation:", err)
	}
	f.service.mu.Lock()
	initialized := f.service.runtimes[child.ID] != nil
	f.service.mu.Unlock()
	if initialized || f.count() != before {
		t.Fatal("creating clone initialized its business environment or called a model")
	}
	selection := sessionSelection(t, f, child.ID)
	if selection.ID != original.ID || selection.Reasoning == nil || selection.Reasoning.Effort != "high" {
		t.Fatalf("clone inherited parent future selection: %+v", selection)
	}
	parent := sessionSelection(t, f, f.session.ID)
	if parent.ID != next.ID || parent.Reasoning == nil || parent.Reasoning.Effort != "low" {
		t.Fatalf("clone changed parent selection: %+v", parent)
	}
	view, err := f.service.ReadContext(f.ctx, child.ID)
	if err != nil {
		t.Fatal("deleted historical model blocked clone history:", err)
	}
	body := string(encode(view.Request.Messages))
	if !strings.Contains(body, "selected historical content") || strings.Contains(body, "parent future content") {
		t.Fatalf("clone included wrong history: %s", body)
	}
	if err = f.service.SelectModel(f.ctx, child.ID, next.ID); err != nil {
		t.Fatal(err)
	}
	if selection = sessionSelection(t, f, child.ID); selection.Reasoning != nil {
		t.Fatal("switching cloned model retained historical reasoning override")
	}
	if _, err = f.service.Submit(f.ctx, child.ID, InputRequest{ID: "child", Text: "continue clone on a current model"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, child.ID)
	if f.count() != before+1 {
		t.Fatal("cloned session failed to continue on a replacement model")
	}
}

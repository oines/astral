package harness

import (
	"context"
	"errors"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/recall"
)

func TestDeleteAgentErasesAllSessionsAndKeepsOtherBots(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("delete-bot-secret") })
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "bot-input", Text: "delete-bot-secret"}); err != nil {
		t.Fatal(err)
	}
	view := f.idle(t, f.session.ID)
	fork, err := f.service.Fork(f.ctx, f.session.ID, view.HeadID, "fork", domain.ID("session"))
	if err != nil {
		t.Fatal(err)
	}
	archiveForDeletion(t, f, fork.ID)
	im, err := f.service.CreateSession(f.ctx, f.agent.ID, "bound unarchived IM history")
	if err != nil {
		t.Fatal(err)
	}
	im.Source, im.BindingID, im.ConversationID = "im", "bound-chat", "chat"
	if err = f.store.Save(f.ctx, "sessions", &im.Meta, &im); err != nil {
		t.Fatal(err)
	}
	other := f.agent
	other.Meta, other.Name = domain.Meta{}, "other Bot"
	if err = f.service.SaveAgent(f.ctx, &other); err != nil {
		t.Fatal(err)
	}
	otherSession, err := f.service.CreateSession(f.ctx, other.ID, "keep this history")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Recall.Search(f.ctx, f.agent.ID, recall.SearchRequest{Query: "delete-bot-secret"}); err != nil {
		t.Fatal(err)
	}
	requests := f.count()
	if err = f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision-1); err == nil {
		t.Fatal("stale confirmation deleted the Bot")
	}
	var unchanged domain.Agent
	if err = f.store.Get(f.ctx, "agents", f.agent.ID, &unchanged); err != nil || unchanged.Deletion {
		t.Fatalf("stale confirmation changed deletion intent: %+v %v", unchanged, err)
	}
	if err = f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err != nil {
		t.Fatal(err)
	}
	if token, err := f.store.StorageReclaimDirty(f.ctx); err != nil || token == "" {
		t.Fatal("Bot deletion committed without a durable space reclamation trigger", err)
	}
	if err = f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err != nil {
		t.Fatal("retry:", err)
	}
	for _, id := range []string{f.session.ID, fork.ID, im.ID} {
		var record domain.Session
		if err = f.store.Get(f.ctx, "sessions", id, &record); !domain.IsMissing(err) {
			t.Fatal("Host Session survived:", id, err)
		}
		if err = f.store.Version(f.ctx, "sessions", id, 1, &record); !domain.IsMissing(err) {
			t.Fatal("Host Session revision survived:", id, err)
		}
		if _, err = f.coreStore.LoadState(f.ctx, id); !errors.Is(err, core.ErrNotFound) {
			t.Fatal("Core Session survived:", id, err)
		}
		if _, err = f.service.View(f.ctx, id); !domain.IsMissing(err) {
			t.Fatal("deleted runtime reopened:", id, err)
		}
	}
	if err = f.store.Version(f.ctx, "agents", f.agent.ID, 1, &unchanged); !domain.IsMissing(err) {
		t.Fatal("Bot revision survived:", err)
	}
	if _, err = f.coreStore.Get(f.ctx, view.RolloutID, view.HeadID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("Core history survived:", err)
	}
	if _, err = f.service.View(f.ctx, otherSession.ID); err != nil {
		t.Fatal("other Bot history was affected:", err)
	}
	if f.count() != requests {
		t.Fatal("deletion invoked a model")
	}
}

func TestDeleteAgentResumesBeforeAndAfterCoreCleanup(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-Core", true: "after-Core"}[after], func(t *testing.T) {
			f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
				t.Error("deletion invoked a model")
				return nil
			})
			f.service.CoreStore = &deleteFaultStore{Store: f.coreStore, deleter: f.coreStore, after: after}
			if err := f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err == nil {
				t.Fatal("fault not surfaced")
			}
			var agent domain.Agent
			var session domain.Session
			if err := f.store.Get(f.ctx, "agents", f.agent.ID, &agent); err != nil || !agent.Deletion {
				t.Fatal("Bot intent lost:", err)
			}
			if err := f.store.Get(f.ctx, "sessions", f.session.ID, &session); err != nil || session.Deletion == nil {
				t.Fatal("Session intent lost:", err)
			}
			if _, err := f.service.CreateSession(f.ctx, agent.ID, "must not reopen"); !domain.IsMissing(err) {
				t.Fatal("new Session admitted during deletion:", err)
			}
			if _, err := f.service.Fork(f.ctx, f.session.ID, "head", "copy", domain.ID("session")); !domain.IsMissing(err) {
				t.Fatal("Fork admitted during deletion:", err)
			}
			if err := f.service.SaveAgent(f.ctx, &f.agent); !domain.IsMissing(err) {
				t.Fatal("configuration cleared deletion intent:", err)
			}
			f.restart(t)
			if err := f.service.ResumeSessionDeletions(f.ctx); err != nil {
				t.Fatal("resume:", err)
			}
			if err := f.store.Get(f.ctx, "agents", agent.ID, &agent); !domain.IsMissing(err) {
				t.Fatal("Bot survived restart cleanup:", err)
			}
			if _, err := f.coreStore.LoadState(f.ctx, session.ID); !errors.Is(err, core.ErrNotFound) {
				t.Fatal("Core Session survived restart cleanup:", err)
			}
		})
	}
}

func TestDeleteAgentStopsActiveRun(t *testing.T) {
	started := make(chan struct{})
	f := newFixture(t, 0, func(ctx context.Context, _ int, _ map[string]any) any {
		close(started)
		<-ctx.Done()
		return nil
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "running-bot", Text: "wait"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if err := f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coreStore.LoadState(f.ctx, f.session.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("active run survived Bot deletion:", err)
	}
}

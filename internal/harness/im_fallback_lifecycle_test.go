package harness

import (
	"context"
	"errors"
	"net/http"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestIMFallbackWaitKeepsDirectReplyWatchAndBlocksProbeRecovery(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("a waiting fallback task dispatched an unsolicited generation")
		return textReply("unexpected")
	})
	chat := imReplyBind(t, f)
	f.agent.ModelAutoRestore = true
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		return modelFallbackTestResponse(request, http.StatusPaymentRequired, `{"error":{"message":"quota exhausted","code":"insufficient_quota"}}`), nil
	})
	imReplyDeliver(t, f, chat, "fallback-direct-question", "mention")
	eventually(t, f.ctx, func() bool {
		var fallback modelFallbackState
		return f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &fallback) == nil && fallback.Phase == "waiting"
	})
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", f.session.ID, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileIMReply(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	var watch imReplyWatch
	if err := f.store.Maintenance(f.ctx, imReplyWatchKind, f.session.ID, &watch); err != nil || len(watch.Inputs) != 1 {
		t.Fatalf("internal fallback failure discarded the direct-reply watch: %+v %v", watch, err)
	}
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Pending direct input alone is not sufficient to recover Probe while the
	// logical task is already waiting for its preferred model to return.
	if _, err := r.core.Load().Steer(f.ctx, core.Input{ID: "queued-during-fallback-wait", Message: core.User("A later direct input.")}); err != nil {
		t.Fatal(err)
	}
	possible, loaded := f.service.IMProbeRecoveryPossible(f.session.ID)
	if !loaded || possible {
		t.Fatalf("waiting fallback became eligible for Probe recovery: possible=%v loaded=%v", possible, loaded)
	}
	if err := f.service.Stop(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileIMReply(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Maintenance(f.ctx, imReplyWatchKind, f.session.ID, &watch); !domain.IsMissing(err) {
		t.Fatalf("an explicitly stopped task retained its reply watch: %v", err)
	}
	if f.count() != 0 {
		t.Fatal("Probe recovery or a reply reminder started work while fallback was waiting")
	}
}

func TestIMProbeRecoveryHintDoesNotOpenColdSession(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("a cold Probe hint dispatched inference")
		return textReply("unexpected")
	})
	cold := domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: f.agent.ID, Title: "Cold query", Source: "web"}
	if err := f.store.Save(f.ctx, "sessions", &cold.Meta, &cold); err != nil {
		t.Fatal(err)
	}
	possible, loaded := f.service.IMProbeRecoveryPossible(cold.ID)
	if possible || loaded {
		t.Fatalf("cold hint reported an initialized runtime: possible=%v loaded=%v", possible, loaded)
	}
	f.service.mu.Lock()
	opened := f.service.runtimes[cold.ID] != nil
	f.service.mu.Unlock()
	if opened {
		t.Fatal("cold Probe hint opened the Session runtime")
	}
	if _, err := f.coreStore.LoadState(f.ctx, cold.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cold Probe hint created Core state: %v", err)
	}
}

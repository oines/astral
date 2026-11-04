package harness

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/messages"
)

type imResumeRecorder struct {
	Messenger
	runtime *runtime
	count   atomic.Int32
	locked  atomic.Bool
}

type pausedIMFixture struct {
	Messenger
	paused atomic.Bool
}

func (p *pausedIMFixture) IMDeliveryError(context.Context, string) (string, error) {
	if p.paused.Load() {
		return "Model provider quota is exhausted. HTTP 402.", nil
	}
	return "", nil
}

func (p *pausedIMFixture) ResumeIMSession(context.Context, string) error {
	p.paused.Store(false)
	return nil
}

func TestPausedIMDeliveryIsVisibleAndExplicitlyResumableAfterRestart(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Explicitly resumed.") })
	pause := &pausedIMFixture{}
	pause.paused.Store(true)
	f.service.IM = pause
	view, err := f.service.View(f.ctx, f.session.ID)
	if err != nil || view.Status != "needs_attention" || view.Error == "" {
		t.Fatalf("an idle Session hid its paused IM delivery: %+v err=%v", view, err)
	}
	f.restart(t)
	subagentRecoveryStopWorker(f)
	f.service.IM = pause
	reopened, err := f.service.View(f.ctx, f.session.ID)
	if err != nil || reopened.Status != "needs_attention" || reopened.Error != view.Error {
		t.Fatalf("reopened Session lost the paused IM failure: %+v err=%v", reopened, err)
	}
	if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "resume-paused-im", Text: "Retry after topping up the provider."}); err != nil {
		t.Fatal(err)
	}
	if pause.paused.Load() {
		t.Fatal("successful explicit input did not clear the paused IM state")
	}
	f.idle(t, f.session.ID)
}

func (r *imResumeRecorder) ResumeIMSession(context.Context, string) error {
	if !r.runtime.mu.TryLock() {
		r.locked.Store(true)
	} else {
		r.runtime.mu.Unlock()
	}
	r.count.Add(1)
	return nil
}

func TestOnlySuccessfulOwnerActionsResumeIMOutsideAdmissionLock(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Finished.") })
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &imResumeRecorder{runtime: r}
	f.service.IM = recorder
	input := InputRequest{ID: "explicit-owner-resume", Text: strings.Repeat("Retained conversation context. ", 4000)}
	if _, err = f.service.Submit(f.ctx, f.session.ID, input); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if _, err = f.service.Submit(f.ctx, f.session.ID, input); err != nil {
		t.Fatal(err)
	}
	observation := core.User("This passive observation must not resume a paused inbox.")
	observation.ContextKind = "im.observation"
	observation.Metadata = encode(messages.Metadata{Source: "im", EventID: "passive-no-resume", ConversationID: "chat"})
	if _, err = f.service.Deliver(f.ctx, f.session.ID, observation, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.service.ResumeContext(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if recorder.count.Load() != 1 {
		t.Fatalf("replay or passive delivery resumed IM: calls=%d", recorder.count.Load())
	}
	if _, err = r.core.Load().Steer(f.ctx, core.Input{ID: "explicit-continue-input", Message: core.User("A pending task.")}); err != nil {
		t.Fatal(err)
	}
	if err = f.service.Continue(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if err = f.service.Compact(f.ctx, f.session.ID, ""); err != nil {
		t.Fatal(err)
	}
	if recorder.count.Load() != 3 || recorder.locked.Load() {
		t.Fatalf("Owner recovery callbacks changed or held the admission lock: count=%d locked=%v", recorder.count.Load(), recorder.locked.Load())
	}
	if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{}); err == nil {
		t.Fatal("invalid submission unexpectedly succeeded")
	}
	if err = f.service.Continue(f.ctx, f.session.ID); err == nil {
		t.Fatal("an idle completed Session unexpectedly continued")
	}
	if recorder.count.Load() != 3 {
		t.Fatal("a rejected Owner operation resumed IM")
	}
}

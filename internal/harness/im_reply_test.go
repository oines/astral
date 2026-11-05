package harness

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
)

func imReplyBind(t *testing.T, f *fixture) domain.Conversation {
	t.Helper()
	chat := promptFixtureChat(t, f, "direct-reply-chat")
	bindPromptFixture(t, f, chat)
	return chat
}

func imReplyDeliver(t *testing.T, f *fixture, chat domain.Conversation, id, reason string) {
	t.Helper()
	event := domain.Observation{ID: id, ExternalEventID: id, ConnectionID: chat.ConnectionID, ConversationID: chat.ID, Kind: "message.created", MessageID: id, Trigger: reason, Text: "Direct question " + id, SentAt: time.Now(), ReceivedAt: time.Now()}
	if prior, err := f.store.Observation(f.ctx, id); err == nil {
		event = prior
	}
	if _, err := f.store.Archive(f.ctx, event); err != nil {
		t.Fatal(err)
	}
	observation, trigger := messages.Observation(event, domain.Connection{}, chat, nil, ""), messages.Trigger(event)
	if _, err := f.service.Deliver(f.ctx, f.session.ID, observation, &trigger); err != nil {
		t.Fatal(err)
	}
}

func imReplySettled(t *testing.T, f *fixture, calls int) {
	t.Helper()
	eventually(t, f.ctx, func() bool {
		var watch imReplyWatch
		return f.count() >= calls && domain.IsMissing(f.store.Maintenance(f.ctx, imReplyWatchKind, f.session.ID, &watch))
	})
	f.idle(t, f.session.ID)
	if f.count() != calls {
		t.Fatalf("unexpected repeat generations: got %d want %d", f.count(), calls)
	}
}

func TestIMMissedDirectReplyAddsOneReminderAndDoesNotLoop(t *testing.T) {
	for _, reason := range []string{"private", "mention", "reply", "poke"} {
		t.Run(reason, func(t *testing.T) {
			f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Text retained in Astral Web.") })
			chat := imReplyBind(t, f)
			imReplyDeliver(t, f, chat, "missed-"+reason, reason)
			imReplySettled(t, f, 2)
			view, err := f.service.ReadContext(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, message := range view.Request.Messages {
				if message.ContextKind == imReplyReminderKind {
					count++
					if message.Role != core.RoleUser || len(message.Content) != 1 || message.Content[0].Text != imMissedReplyReminder {
						t.Fatalf("invalid reminder: %+v", message)
					}
				}
			}
			if count != 1 {
				t.Fatalf("got %d reminders", count)
			}
			imReplyDeliver(t, f, chat, "missed-"+reason, reason)
			imReplySettled(t, f, 2)
		})
	}
}

func TestIMReplyDoesNotRemindAfterStartedSendEvenWhenSendFails(t *testing.T) {
	f := newFixture(t, 0, func(_ context.Context, n int, _ map[string]any) any {
		if n == 1 {
			return toolReply("failed-send", "send_message", map[string]any{"text": "Attempted reply."})
		}
		return textReply("The attempted send failed; avoid another automatic send.")
	})
	chat := imReplyBind(t, f)
	imReplyDeliver(t, f, chat, "send-failed", "mention")
	imReplySettled(t, f, 2)
	for _, entry := range subagentTestEntries(t, f, f.session.ID) {
		if entry.Message != nil && entry.Message.ContextKind == imReplyReminderKind {
			t.Fatal("a failed send_message was automatically retried")
		}
	}
}

func TestIMReplyNewDirectAfterEarlierSendStillGetsOneCombinedReminder(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	releaseModel := sync.OnceFunc(func() { close(release) })
	defer releaseModel()
	f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
		switch n {
		case 1:
			return toolReply("old-send", "send_message", map[string]any{"text": "Reply to earlier work."})
		case 2:
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
			return toolReply("next-request", "exec_command", map[string]any{"cmd": "unbound fixture", "login": false})
		}
		return textReply("Only Web text for the newly consumed questions.")
	})
	chat := imReplyBind(t, f)
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "earlier-work", Text: "Start the task."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	imReplyDeliver(t, f, chat, "new-direct-a", "mention")
	imReplyDeliver(t, f, chat, "new-direct-b", "reply")
	releaseModel()
	imReplySettled(t, f, 4)
	count := 0
	for _, entry := range subagentTestEntries(t, f, f.session.ID) {
		if entry.Message != nil && entry.Message.ContextKind == imReplyReminderKind {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("multiple direct inputs were not combined: %d reminders", count)
	}
}

func TestIMReplyWebTriggerLikeIDAndArchivedHistoryDoNotRegister(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Normal Web reply.") })
	chat := imReplyBind(t, f)
	event := domain.Observation{ID: "web-chosen", ConversationID: chat.ID, Trigger: "mention", Text: "Archived older message."}
	if _, err := f.store.Archive(f.ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: event.ID + ":trigger", Text: "This is Web input."}); err != nil {
		t.Fatal(err)
	}
	imReplySettled(t, f, 1)
}

func TestIMReplyReminderPendingReceiptRecoversOnceAndStoppedDoesNotResume(t *testing.T) {
	for _, stage := range []string{"accepted-before-continue", "started-before-consumption", "stopped"} {
		t.Run(stage, func(t *testing.T) {
			stop := stage == "stopped"
			entered := make(chan struct{})
			f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
				if stop && n == 2 {
					close(entered)
					<-ctx.Done()
					return nil
				}
				return textReply("Text only.")
			})
			chat := imReplyBind(t, f)
			subagentRecoveryStopWorker(f)
			imReplyDeliver(t, f, chat, "recovery-direct", "private")
			f.idle(t, f.session.ID)
			r := f.service.runtimes[f.session.ID]
			runID := r.core.Load().State().LastRun.ID
			watch := imReplyWatch{AgentID: f.agent.ID, SessionID: f.session.ID, BindingID: "binding-" + chat.ID, ConversationID: chat.ID, RunID: runID, ReminderID: "im_reply_reminder_" + runID}
			if err := f.store.SaveMaintenance(f.ctx, imReplyWatchKind, f.session.ID, "pending", watch); err != nil {
				t.Fatal(err)
			}
			message := core.User(imMissedReplyReminder)
			message.ContextKind = imReplyReminderKind
			message.Metadata = encode(messages.Metadata{Source: imReplyReminderKind, DeliveryID: watch.ReminderID, ConversationID: chat.ID})
			if _, err := r.core.Load().Steer(f.ctx, core.Input{ID: watch.ReminderID, Message: message}); err != nil {
				t.Fatal(err)
			}
			if stage == "started-before-consumption" {
				// Crash boundary: Continue's durable start exists, while drive has
				// not yet frozen a request or consumed the accepted reminder.
				state := r.core.Load().State()
				runID := domain.ID("run")
				entry := core.Entry{ID: domain.ID("entry"), SessionID: f.session.ID, Version: state.Version + 1, Time: time.Now().UTC(), Kind: core.EntryRun, RunID: runID, Run: &core.RunRecord{ID: runID, Status: "started"}}
				if _, err := f.coreStore.Commit(f.ctx, f.session.ID, core.Commit{ID: domain.ID("commit"), ExpectedVersion: state.Version, Entries: []core.Entry{entry}}); err != nil {
					t.Fatal(err)
				}
			}
			if stop {
				if _, err := r.core.Load().Continue(f.ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case <-entered:
				case <-f.ctx.Done():
					t.Fatal(f.ctx.Err())
				}
				if err := f.service.Stop(f.ctx, f.session.ID); err != nil {
					t.Fatal(err)
				}
				f.idle(t, f.session.ID)
			}
			f.restart(t)
			imReplySettled(t, f, 2)
			if !stop {
				f.restart(t)
				imReplySettled(t, f, 2)
			}
		})
	}
}

type imReplySparseStore struct {
	core.Store
	t *testing.T
}

func (s imReplySparseStore) Get(context.Context, string, string) (core.Record, error) {
	s.t.Error("reply inspection loaded a history body")
	return core.Record{}, errors.New("reply inspection must not read message bodies")
}

func (s imReplySparseStore) Scan(ctx context.Context, rollout string, query core.Query) (core.Page, error) {
	if query.Kind != core.EntryInputConsumed && query.Kind != core.EntryToolStarted || query.Limit > 64 || query.MaxBytes > 1<<20 || !query.Reverse {
		s.t.Errorf("unbounded reply inspection: %+v", query)
	}
	return s.Store.Scan(ctx, rollout, query)
}

func TestIMReplyInspectionUsesOnlySmallDurableFacts(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Only text.") })
	chat := imReplyBind(t, f)
	subagentRecoveryStopWorker(f)
	imReplyDeliver(t, f, chat, "sparse-inspection", "mention")
	f.idle(t, f.session.ID)
	f.service.CoreStore = imReplySparseStore{Store: f.coreStore, t: t}
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", f.session.ID, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileIMReply(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	f.service.CoreStore = f.coreStore
	f.idle(t, f.session.ID)
	if err := f.service.reconcileIMReply(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	imReplySettled(t, f, 2)
	f.mu.Lock()
	request := string(f.requests[len(f.requests)-1])
	f.mu.Unlock()
	if !strings.Contains(request, "只显示在网页") {
		t.Fatal("the actual request did not receive the follow-up reminder")
	}
}

func TestIMReplyOlderDirectDoesNotMakeLaterUnrelatedRunRequireReply(t *testing.T) {
	for _, source := range []string{"web", "im.wakeup", subagentUpdateKind, "stop", "rebind"} {
		t.Run(source, func(t *testing.T) {
			f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Text only.") })
			chat := imReplyBind(t, f)
			subagentRecoveryStopWorker(f)
			imReplyDeliver(t, f, chat, "old-direct", "mention")
			f.idle(t, f.session.ID)
			want := 1
			switch source {
			case "stop":
				if err := f.service.Stop(f.ctx, f.session.ID); err != nil {
					t.Fatal(err)
				}
			case "rebind":
				bindPromptFixture(t, f, promptFixtureChat(t, f, "replacement-chat"))
			default:
				message := core.User("An unrelated later input.")
				message.ContextKind = source
				r := f.service.runtimes[f.session.ID]
				if _, err := r.core.Load().Steer(f.ctx, core.Input{ID: "unrelated-" + source, Message: message}); err != nil {
					t.Fatal(err)
				}
				run, err := r.core.Load().Continue(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := run.Wait(f.ctx); err != nil {
					t.Fatal(err)
				}
				want = 2
			}
			var record domain.Session
			if err := f.store.Get(f.ctx, "sessions", f.session.ID, &record); err != nil {
				t.Fatal(err)
			}
			if err := f.service.reconcileIMReply(f.ctx, record); err != nil {
				t.Fatal(err)
			}
			imReplySettled(t, f, want)
		})
	}
}

func TestIMReplyWorkerRetriesAfterReadOnlyAdmissionLock(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	releaseModel := sync.OnceFunc(func() { close(release) })
	defer releaseModel()
	f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
		}
		return textReply("Text only.")
	})
	chat := imReplyBind(t, f)
	imReplyDeliver(t, f, chat, "busy-reader", "private")
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	r := f.service.runtimes[f.session.ID]
	r.mu.Lock()
	releaseModel()
	eventually(t, f.ctx, func() bool {
		state := r.core.Load().State()
		f.service.subagentWakeMu.Lock()
		defer f.service.subagentWakeMu.Unlock()
		return state.LastRun != nil && state.LastRun.Status == "completed" && len(f.service.subagentDirty) == 0
	})
	r.mu.Unlock() // This read-only operation produces no notification.
	imReplySettled(t, f, 2)
}

func TestIMReplyStaleBindingRecordDoesNotClearNewDirectWatch(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Text only.") })
	imReplyBind(t, f)
	subagentRecoveryStopWorker(f)
	var stale domain.Session
	if err := f.store.Get(f.ctx, "sessions", f.session.ID, &stale); err != nil {
		t.Fatal(err)
	}
	chat := promptFixtureChat(t, f, "new-bound-chat")
	bindPromptFixture(t, f, chat)
	imReplyDeliver(t, f, chat, "new-bound-direct", "mention")
	f.idle(t, f.session.ID)
	if err := f.service.reconcileIMReply(f.ctx, stale); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if err := f.service.reconcileIMReply(f.ctx, stale); err != nil {
		t.Fatal(err)
	}
	imReplySettled(t, f, 2)
}

func TestIMReplyFailedDirectRunDoesNotRemindOrResumeAfterRestart(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("failed request reached the ordinary fixture provider")
		return nil
	})
	chat := imReplyBind(t, f)
	var failures atomic.Int32
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		failures.Add(1)
		return &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":"Fixture authentication failure"}}`)), Request: request}, nil
	})
	imReplyDeliver(t, f, chat, "failed-direct", "private")
	eventually(t, f.ctx, func() bool {
		state, err := f.coreStore.LoadState(f.ctx, f.session.ID)
		var watch imReplyWatch
		return err == nil && state.LastRun != nil && state.LastRun.Status == "failed" && domain.IsMissing(f.store.Maintenance(f.ctx, imReplyWatchKind, f.session.ID, &watch))
	})
	f.restart(t)
	if view, err := f.service.View(f.ctx, f.session.ID); err != nil || view.LastRun == nil || view.LastRun.Status != "failed" || view.RunID != "" {
		t.Fatalf("failed original direct changed after reopening: %+v %v", view, err)
	}
	if failures.Load() != 1 {
		t.Fatalf("failed original direct was retried: %d requests", failures.Load())
	}
}

func TestIMReplyIdleFlushWaitsForItsActualDirectHandoffGeneration(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	releaseModel := sync.OnceFunc(func() { close(release) })
	defer releaseModel()
	f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
		}
		return textReply("Text only.")
	})
	chat := imReplyBind(t, f)
	subagentRecoveryStopWorker(f)
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "prior-web", Text: "Finish the current request."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	imReplyDeliver(t, f, chat, "arrived-after-freeze", "mention")
	releaseModel()
	f.idle(t, f.session.ID)
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", f.session.ID, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileIMReply(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatal("an unconsumed direct caused a reminder for the frozen earlier request")
	}
	event, err := f.store.Observation(f.ctx, "arrived-after-freeze")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.WakeObservations(f.ctx, f.session.ID, []IMWakeup{{InputID: event.ID + ":trigger", Message: messages.Trigger(event)}}, record.BindingID); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if err := f.service.reconcileIMReply(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if err := f.service.reconcileIMReply(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	imReplySettled(t, f, 3)
}

func TestIMReplyWatchSurvivesStartedRunUntilReminderIsConsumed(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Text only.") })
	chat := imReplyBind(t, f)
	subagentRecoveryStopWorker(f)
	imReplyDeliver(t, f, chat, "gated-reminder", "mention")
	f.idle(t, f.session.ID)
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", f.session.ID, &record); err != nil {
		t.Fatal(err)
	}
	r := f.service.runtimes[f.session.ID]
	entered, release := make(chan struct{}), make(chan struct{})
	releasePreparation := sync.OnceFunc(func() { close(release) })
	defer releasePreparation()
	config := r.coreConfig(record)
	resolve := config.ResolveModel
	config.ResolveModel = func(ctx context.Context, selection core.ModelSelection) (core.ResolvedModel, error) {
		close(entered)
		select {
		case <-release:
			return resolve(ctx, selection)
		case <-ctx.Done():
			return core.ResolvedModel{}, ctx.Err()
		}
	}
	r.mu.Lock()
	if err := r.closeCore(f.ctx); err != nil {
		r.mu.Unlock()
		t.Fatal(err)
	}
	if err := r.reopenCore(f.ctx, config); err != nil {
		r.mu.Unlock()
		t.Fatal(err)
	}
	r.mu.Unlock()
	if err := f.service.reconcileIMReply(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	var watch imReplyWatch
	if err := f.store.Maintenance(f.ctx, imReplyWatchKind, record.ID, &watch); err != nil || watch.ReminderID == "" {
		t.Fatalf("Run.started discarded the unconsumed reminder's recovery identity: %+v %v", watch, err)
	}
	if receipt, err := f.coreStore.LookupInput(f.ctx, record.ID, watch.ReminderID); err != nil || receipt.State != core.InputPending {
		t.Fatalf("fixture did not hold the exact reminder before consumption: %+v %v", receipt, err)
	}
	releasePreparation()
	f.idle(t, record.ID)
	if err := f.service.reconcileIMReply(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	imReplySettled(t, f, 2)
}

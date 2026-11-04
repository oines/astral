package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/store/memory"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
)

type recoveryBatchReads struct {
	attempts, heads, paths int
}

// Count reads of one real frozen request, rather than timing a mock inspector.
// Core reconstructs that request through GetAttempt and its immutable head.
type recoveryBatchStore struct {
	core.Store
	mu        sync.Mutex
	attemptID string
	headID    string
	reads     recoveryBatchReads
}

func (s *recoveryBatchStore) watch(attempt core.ModelAttempt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attemptID, s.headID, s.reads = attempt.ID, attempt.Input.HeadID, recoveryBatchReads{}
}

func (s *recoveryBatchStore) measured() recoveryBatchReads {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func (s *recoveryBatchStore) GetAttempt(ctx context.Context, id, attemptID string) (core.ModelAttempt, error) {
	s.mu.Lock()
	if attemptID == s.attemptID {
		s.reads.attempts++
	}
	s.mu.Unlock()
	return s.Store.GetAttempt(ctx, id, attemptID)
}

func (s *recoveryBatchStore) Get(ctx context.Context, rolloutID, entryID string) (core.Record, error) {
	s.mu.Lock()
	if entryID == s.headID {
		s.reads.heads++
	}
	s.mu.Unlock()
	return s.Store.Get(ctx, rolloutID, entryID)
}

func (s *recoveryBatchStore) Scan(ctx context.Context, rolloutID string, query core.Query) (core.Page, error) {
	s.mu.Lock()
	if query.PathHead == s.headID && query.PathHead != "" && query.Cursor == "" {
		s.reads.paths++
	}
	s.mu.Unlock()
	return s.Store.Scan(ctx, rolloutID, query)
}

func recoveryBatchObservation(t testing.TB, ctx context.Context, session *core.Session, chatID, id string) IMWakeup {
	t.Helper()
	event := domain.Observation{ID: id, ConversationID: chatID, MessageID: id, Text: "Synthetic passive observation " + id, Trigger: "mention", SentAt: time.Now()}
	observation := messages.Observation(event, domain.Connection{}, domain.Conversation{Meta: domain.Meta{ID: chatID}}, nil, "")
	if _, err := session.InjectContext(ctx, core.Input{ID: id, Contexts: []core.Message{observation}}); err != nil {
		t.Fatal(err)
	}
	return IMWakeup{InputID: id, Message: messages.Trigger(event)}
}

func recoveryBatchAttempt(t testing.TB, ctx context.Context, session *core.Session, observations int) core.ModelAttempt {
	t.Helper()
	page, err := session.Attempts().Scan(ctx, core.AttemptQuery{Purpose: "generation", Reverse: true, Limit: 1})
	if err != nil || len(page.Attempts) != 1 {
		t.Fatalf("missing historical generation: %+v %v", page, err)
	}
	attempt := page.Attempts[0]
	request, err := session.Attempts().Request(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	events := map[string]bool{}
	for _, message := range request.Messages {
		var metadata messages.Metadata
		if message.ContextKind == "im.observation" && json.Unmarshal(message.Metadata, &metadata) == nil {
			events[metadata.EventID] = true
		}
	}
	if len(events) != observations {
		t.Fatalf("fixture generation actually saw %d observations, want %d", len(events), observations)
	}
	return attempt
}

func TestIMRecoveryBatchRebuildsSharedHistoryOnceAndStillWakesNewInput(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Handled the visible observations.") })
	// An already opened Core handle retains its Store. Reopen the Host with the
	// instrumented Store before creating the history whose reads we measure.
	models := f.service.Models
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	counted := &recoveryBatchStore{Store: f.coreStore}
	f.service = New(f.store, counted, models, f.hub)
	chat := promptFixtureChat(t, f, "recovery-batch-chat")
	bindPromptFixture(t, f, chat)
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	const size = 64
	inputs := make([]IMWakeup, size)
	for i := range inputs {
		inputs[i] = recoveryBatchObservation(t, f.ctx, session, chat.ID, fmt.Sprintf("covered-%02d", i))
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "historical-driver", Text: "Process all currently visible observations."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	attempt := recoveryBatchAttempt(t, f.ctx, session, size)
	counted.watch(attempt)
	before, requests := session.State().Version, f.count()
	statuses, err := f.service.WakeObservations(f.ctx, f.session.ID, inputs, "binding-"+chat.ID)
	if err != nil || len(statuses) != size {
		t.Fatalf("recover covered page: statuses=%v err=%v", statuses, err)
	}
	for i, status := range statuses {
		if status != IMWakeCovered {
			t.Fatalf("covered observation %d was scheduled again: %s", i, status)
		}
	}
	if got := counted.measured(); got != (recoveryBatchReads{attempts: 1, heads: 1, paths: 1}) {
		t.Fatalf("one generation covers %d inputs, but its request was reconstructed repeatedly: %+v", size, got)
	}
	if session.State().Version != before || f.count() != requests {
		t.Fatal("auditing already handled observations wrote history or dispatched the model")
	}

	newInput := recoveryBatchObservation(t, f.ctx, session, chat.ID, "unseen-after-generation")
	statuses, err = f.service.WakeObservations(f.ctx, f.session.ID, []IMWakeup{inputs[0], newInput}, "binding-"+chat.ID)
	if err != nil || len(statuses) != 2 || statuses[0] != IMWakeCovered || statuses[1] != IMWakeAccepted {
		t.Fatalf("the cached historical request hid a new observation: statuses=%v err=%v", statuses, err)
	}
	f.idle(t, f.session.ID)
	if f.count() != requests+1 {
		t.Fatalf("new observation did not start exactly one real generation: before=%d after=%d", requests, f.count())
	}
	receipt, err := session.LookupInput(f.ctx, newInput.InputID+":wakeup")
	if err != nil || receipt.Kind != core.InputSteer || receipt.State != core.InputApplied {
		t.Fatalf("new observation's wakeup was not consumed: %+v %v", receipt, err)
	}
	recoveryBatchAttempt(t, f.ctx, session, size+1)
}

type recoveryBatchMemory struct {
	ctx     context.Context
	service *Service
	session *core.Session
	store   *recoveryBatchStore
	calls   atomic.Int64
}

// The covered/busy/terminal paths need only a real Core and Host admission
// records. No Provider HTTP server or model-resolution fixture is involved.
func newRecoveryBatchMemory(t testing.TB, generate func(context.Context, core.Request) (core.Reply, error)) *recoveryBatchMemory {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	f := &recoveryBatchMemory{ctx: ctx, store: &recoveryBatchStore{Store: memory.New()}}
	hostStore, err := persistence.Open(t.TempDir())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	model := core.ModelFunc{Specification: core.ModelSpec{ID: "recovery-batch", ContextWindow: 128000, MaxOutputTokens: 4096}, Generate: func(ctx context.Context, request core.Request, _ func(core.ModelEvent)) (core.Reply, error) {
		f.calls.Add(1)
		if generate != nil {
			return generate(ctx, request)
		}
		return core.Reply{Message: core.Message{Role: core.RoleAssistant, Content: core.Text("Handled."), FinishReason: core.FinishStop}}, nil
	}}
	f.session, err = core.NewSession(ctx, core.Config{
		Store: f.store, Model: model, SystemPrompt: "Synthetic IM recovery history.", Compaction: core.CompactionPolicy{Disabled: true},
		EstimateTokens: func(_ context.Context, _ core.Model, request core.Request) (int, error) {
			return 100 * len(request.Messages), nil
		},
	}, core.SessionOptions{ID: "recovery-session"})
	if err != nil {
		cancel()
		_ = hostStore.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := f.session.Close(cleanup); err != nil {
			t.Error(err)
		}
		if err := hostStore.Close(); err != nil {
			t.Error(err)
		}
	})
	agent := domain.Agent{Meta: domain.Meta{ID: "recovery-agent"}, Name: "Synthetic recovery Bot"}
	if err := hostStore.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
		t.Fatal(err)
	}
	record := domain.Session{Meta: domain.Meta{ID: f.session.ID()}, AgentID: agent.ID, Source: "im", BindingID: "recovery-binding", ConversationID: "recovery-chat"}
	if err := hostStore.Save(ctx, "sessions", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	f.service = &Service{Store: hostStore, CoreStore: f.store, ctx: ctx, changes: make(chan string, 1), runtimes: map[string]*runtime{}}
	r := &runtime{service: f.service, id: record.ID, agentID: agent.ID, ready: make(chan struct{})}
	r.core.Store(f.session)
	close(r.ready)
	f.service.runtimes[record.ID] = r
	return f
}

func TestIMRecoveryBatchStopsAtBusyWithoutInspectingTail(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := newRecoveryBatchMemory(t, func(ctx context.Context, _ core.Request) (core.Reply, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return core.Reply{}, ctx.Err()
		case <-release:
			return core.Reply{Message: core.Message{Role: core.RoleAssistant, Content: core.Text("Finished."), FinishReason: core.FinishStop}}, nil
		}
	})
	if _, err := f.session.Steer(f.ctx, core.Input{ID: "active-driver", Message: core.User("Start work.")}); err != nil {
		t.Fatal(err)
	}
	run, err := f.session.Continue(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	input := recoveryBatchObservation(t, f.ctx, f.session, "recovery-chat", "arrived-during-generation")
	// This input deliberately does not exist. Inspecting beyond the first Busy
	// would return ErrNotFound instead of leaving the rest of the page untouched.
	statuses, err := f.service.WakeObservations(f.ctx, f.session.ID(), []IMWakeup{input, {InputID: "uninspected-tail"}}, "recovery-binding")
	if err != nil || len(statuses) != 1 || statuses[0] != IMWakeBusy {
		t.Fatalf("busy page did not stop at the first deferred input: statuses=%v err=%v", statuses, err)
	}
	if _, err := f.session.LookupInput(f.ctx, input.InputID+":wakeup"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("busy observation admitted another driver: %v", err)
	}
	close(release)
	if _, err := run.Wait(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIMRecoveryBatchRejectsChangedBindingWithoutAdmission(t *testing.T) {
	f := newRecoveryBatchMemory(t, nil)
	input := recoveryBatchObservation(t, f.ctx, f.session, "recovery-chat", "old-binding-input")
	before := f.session.State().Version
	statuses, err := f.service.WakeObservations(f.ctx, f.session.ID(), []IMWakeup{input}, "replaced-binding")
	var conflict *domain.Error
	if !errors.As(err, &conflict) || conflict.Status != 409 || len(statuses) != 0 {
		t.Fatalf("stale binding was allowed to recover input: statuses=%v err=%v", statuses, err)
	}
	if f.session.State().Version != before || f.calls.Load() != 0 {
		t.Fatal("stale binding modified the Session or dispatched a model")
	}
	if _, err := f.session.LookupInput(f.ctx, input.InputID+":wakeup"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("stale binding left a wakeup receipt: %v", err)
	}
}

func TestIMRecoveryBatchDoesNotReplayStoppedOrFailedInputs(t *testing.T) {
	for _, status := range []string{"cancelled", "failed"} {
		t.Run(status, func(t *testing.T) {
			entered := make(chan struct{})
			f := newRecoveryBatchMemory(t, func(ctx context.Context, _ core.Request) (core.Reply, error) {
				close(entered)
				if status == "cancelled" {
					<-ctx.Done()
					return core.Reply{}, ctx.Err()
				}
				return core.Reply{}, errors.New("synthetic generation failure")
			})
			input := recoveryBatchObservation(t, f.ctx, f.session, "recovery-chat", "before-terminal-run")
			if _, err := f.session.Steer(f.ctx, core.Input{ID: "terminal-driver", Message: core.User("Attempt the visible observation.")}); err != nil {
				t.Fatal(err)
			}
			run, err := f.session.Continue(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			if status == "cancelled" {
				run.Cancel()
			}
			result, runErr := run.Wait(f.ctx)
			if result.Status != status || f.ctx.Err() != nil {
				t.Fatalf("fixture terminal state: %+v %v", result, runErr)
			}
			before := f.session.State().Version
			statuses, err := f.service.WakeObservations(f.ctx, f.session.ID(), []IMWakeup{input}, "recovery-binding")
			if err != nil || len(statuses) != 1 || statuses[0] != IMWakeCovered || f.session.State().Version != before || f.calls.Load() != 1 {
				t.Fatalf("%s input was replayed: statuses=%v calls=%d err=%v", status, statuses, f.calls.Load(), err)
			}
			if _, err := f.session.LookupInput(f.ctx, input.InputID+":wakeup"); !errors.Is(err, core.ErrNotFound) {
				t.Fatalf("%s input left another driver: %v", status, err)
			}
			fresh := recoveryBatchObservation(t, f.ctx, f.session, "recovery-chat", "after-terminal-run")
			if handled, err := NewIMInspector(f.session, f.store).Handled(f.ctx, fresh.InputID); err != nil || handled {
				t.Fatalf("%s history incorrectly covered a later observation: handled=%v err=%v", status, handled, err)
			}
		})
	}
}

func BenchmarkIMRecoveryCoveredPage(b *testing.B) {
	for _, batch := range []bool{true, false} {
		name := "batch64"
		if !batch {
			name = "one_at_a_time64"
		}
		b.Run(name, func(b *testing.B) {
			f := newRecoveryBatchMemory(b, nil)
			inputs := make([]IMWakeup, 64)
			for i := range inputs {
				inputs[i] = recoveryBatchObservation(b, f.ctx, f.session, "recovery-chat", fmt.Sprintf("covered-%02d", i))
			}
			if _, err := f.session.Steer(f.ctx, core.Input{ID: "historical-driver", Message: core.User("Handle the visible observations.")}); err != nil {
				b.Fatal(err)
			}
			run, err := f.session.Continue(f.ctx)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := run.Wait(f.ctx); err != nil {
				b.Fatal(err)
			}
			f.store.watch(recoveryBatchAttempt(b, f.ctx, f.session, len(inputs)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				width := len(inputs)
				if !batch {
					width = 1
				}
				for at := 0; at < len(inputs); at += width {
					statuses, err := f.service.WakeObservations(f.ctx, f.session.ID(), inputs[at:at+width], "recovery-binding")
					if err != nil || len(statuses) != width {
						b.Fatalf("recover page: statuses=%v err=%v", statuses, err)
					}
					for _, status := range statuses {
						if status != IMWakeCovered {
							b.Fatalf("historical observation was not covered: %s", status)
						}
					}
				}
			}
			b.StopTimer()
			want := b.N
			if !batch {
				want *= len(inputs)
			}
			if got := f.store.measured(); got.attempts != want || got.heads != want || got.paths != want || f.calls.Load() != 1 {
				b.Fatalf("audit did not measure the expected frozen-request reconstruction: reads=%+v want=%d model_calls=%d", got, want, f.calls.Load())
			}
			b.ReportMetric(float64(want)/float64(b.N), "rebuilds/page")
		})
	}
}

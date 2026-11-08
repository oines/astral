package harness

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func schedulerWaitingFixture(t *testing.T) (*fixture, *atomic.Int64) {
	t.Helper()
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("a waiting request escaped its offline transport")
		return nil
	})
	f.agent.ModelAutoRestore = true
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	probes := new(atomic.Int64)
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		_, raw := modelFallbackTestRequest(t, request)
		if strings.Contains(raw, "Check that generation is available.") {
			probes.Add(1)
		}
		return modelFallbackTestResponse(request, http.StatusPaymentRequired, `{"error":{"message":"synthetic quota exhausted","code":"insufficient_quota"}}`), nil
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "scheduler-waiting", Text: "Finish this synthetic task when its model returns."}); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		var state modelFallbackState
		return f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state) == nil && state.Phase == "waiting"
	})
	t.Cleanup(func() {
		if err := f.service.Stop(f.ctx, f.session.ID); err != nil {
			t.Error(err)
		}
	})
	return f, probes
}

func schedulerSetProbeAt(t *testing.T, f *fixture, at time.Time) {
	t.Helper()
	f.service.modelFallbackMu.Lock()
	defer f.service.modelFallbackMu.Unlock()
	var state modelFallbackState
	if err := f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state); err != nil {
		t.Fatal(err)
	}
	state.NextProbeAt = at
	if err := f.service.saveModelFallback(f.ctx, state); err != nil {
		t.Fatal(err)
	}
}

func TestFallbackSchedulerKeepsFutureWaitingSessionColdAfterRestart(t *testing.T) {
	f, probes := schedulerWaitingFixture(t)
	schedulerSetProbeAt(t, f, time.Now().Add(time.Hour))
	f.restart(t)
	// A previously queued terminal/resource wake may still reach reconciliation
	// after the persisted deadline was postponed. It must remain a cheap no-op,
	// even when it bypasses the scheduler's in-memory deadline selection.
	if err := f.service.reconcileModelFallback(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * modelFallbackTick)
	f.service.mu.Lock()
	_, opened := f.service.runtimes[f.session.ID]
	f.service.mu.Unlock()
	if opened || probes.Load() != 0 {
		t.Fatalf("future detection opened a cold Session or called the model: opened=%v probes=%d", opened, probes.Load())
	}
	schedulerSetProbeAt(t, f, time.Now().Add(-time.Second))
	eventually(t, f.ctx, func() bool { return probes.Load() == 1 })
}

func TestFallbackSchedulerAcceptsPostponedAndAdvancedDeadline(t *testing.T) {
	f, probes := schedulerWaitingFixture(t)
	schedulerSetProbeAt(t, f, time.Now().Add(2*modelFallbackTick))
	schedulerSetProbeAt(t, f, time.Now().Add(time.Hour))
	time.Sleep(3 * modelFallbackTick)
	if probes.Load() != 0 {
		t.Fatal("the old deadline survived a postponed detection")
	}
	schedulerSetProbeAt(t, f, time.Now().Add(-time.Second))
	eventually(t, f.ctx, func() bool { return probes.Load() == 1 })
	eventually(t, f.ctx, func() bool {
		var state modelFallbackState
		return f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state) == nil && state.Phase == "waiting" && state.NextProbeAt.After(time.Now())
	})
}

func TestFallbackSchedulerPolicyChangeExhaustsWaitingWithoutRevivingIt(t *testing.T) {
	f, probes := schedulerWaitingFixture(t)
	f.agent.ModelAutoRestore = false
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		var state modelFallbackState
		return f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state) == nil && state.Phase == "exhausted"
	})
	f.agent.ModelAutoRestore = true
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * modelFallbackTick)
	var state modelFallbackState
	if err := f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state); err != nil || state.Phase != "exhausted" || probes.Load() != 0 {
		t.Fatalf("policy enable revived exhausted work: phase=%s probes=%d err=%v", state.Phase, probes.Load(), err)
	}
}

func TestFallbackSchedulerPolicyEnableArmsActiveBackupDetection(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var startOnce, releaseOnce sync.Once
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if request["model"] != "fixture-next" {
			t.Error("conversation did not use its backup model")
		}
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
			return textReply("The backup finished its frozen request.")
		case <-ctx.Done():
			return nil
		}
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	backup := anotherModel(t, f)
	f.agent.ModelPriority = []core.ModelSelection{{ID: f.agent.DefaultModelID}, {ID: backup.ID}}
	f.agent.ModelAutoRestore = false
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	var probes atomic.Int64
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		decoded, raw := modelFallbackTestRequest(t, request)
		if decoded["model"] != "fixture" {
			return original.RoundTrip(request)
		}
		if strings.Contains(raw, "Check that generation is available.") {
			probes.Add(1)
			return modelFallbackTestResponse(request, http.StatusOK, string(encode(textReply("OK")))), nil
		}
		return modelFallbackTestResponse(request, http.StatusPaymentRequired, `{"error":{"message":"synthetic quota exhausted","code":"insufficient_quota"}}`), nil
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "scheduler-enable", Text: "Continue with the backup while the preferred model is unavailable."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	schedulerSetProbeAt(t, f, time.Now().Add(-time.Second))
	time.Sleep(2 * modelFallbackTick)
	if probes.Load() != 0 {
		t.Fatal("disabled automatic restoration made a detection request")
	}
	f.agent.ModelAutoRestore = true
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		var state modelFallbackState
		return probes.Load() == 1 && f.store.Maintenance(f.ctx, modelFallbackKind, f.session.ID, &state) == nil && state.Active.ID == f.agent.DefaultModelID
	})
	releaseOnce.Do(func() { close(release) })
	f.idle(t, f.session.ID)
}

func TestFallbackSchedulerClassifiesOrdinaryActiveTerminalValidationFailure(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		reply := toolReply("duplicate", "synthetic_tool", map[string]any{}).(map[string]any)
		message := reply["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
		calls := message["tool_calls"].([]any)
		message["tool_calls"] = append(calls, calls[0])
		return reply
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "scheduler-invalid-terminal", Text: "Complete this synthetic task."}); err != nil {
		t.Fatal(err)
	}
	// No View/Pending call may clean this up on behalf of the terminal event.
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	eventually(t, ctx, func() bool {
		var state modelFallbackState
		return f.store.Maintenance(ctx, modelFallbackKind, f.session.ID, &state) == nil && state.Phase == "exhausted"
	})
	if f.count() != 1 {
		t.Fatal("terminal validation classification repeated ordinary work")
	}
}

// Exercise the real queue/deadline/Store loop while a controlled executor owns
// one Session. It makes a wake-during-reconcile interleaving deterministic.
func schedulerLoopFixture(t *testing.T, reconcile func(context.Context, string) error) (*Service, *domain.Agent) {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	agent := domain.Agent{Meta: domain.Meta{ID: "synthetic-scheduler-agent"}, Name: "Synthetic scheduler policy"}
	if err := store.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
		t.Fatal(err)
	}
	state := modelFallbackState{SessionID: "synthetic-scheduler-session", AgentID: agent.ID, Phase: "active",
		Preferred: core.ModelSelection{ID: "synthetic-model"}, Active: core.ModelSelection{ID: "synthetic-model"}}
	if err := store.SaveMaintenance(ctx, modelFallbackKind, state.SessionID, "active", state); err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: store, ctx: ctx, cancel: cancel, modelFallbackUpdates: map[string]modelFallbackScheduleUpdate{}, modelFallbackWake: make(chan struct{}, 1)}
	s.modelFallbackWorkers.Add(1)
	go s.runModelFallbackWorker(reconcile)
	t.Cleanup(func() {
		cancel()
		done := make(chan struct{})
		go func() { s.modelFallbackWorkers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("scheduler did not stop")
		}
	})
	return s, &agent
}

func TestFallbackSchedulerKeepsForceReceivedDuringActiveWorker(t *testing.T) {
	first, second, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	s, _ := schedulerLoopFixture(t, func(ctx context.Context, _ string) error {
		switch calls.Add(1) {
		case 1:
			close(first)
			select {
			case <-release:
			case <-ctx.Done():
			}
		case 2:
			close(second)
		}
		return nil
	})
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("first reconcile did not start")
	}
	s.wakeModelFallback("synthetic-scheduler-session")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eventually(t, ctx, func() bool {
		s.modelFallbackScheduleMu.Lock()
		defer s.modelFallbackScheduleMu.Unlock()
		return len(s.modelFallbackUpdates) == 0
	})
	close(release)
	select {
	case <-second:
	case <-ctx.Done():
		t.Fatal("done consumed an explicit wake received by the active worker")
	}
	time.Sleep(2 * modelFallbackTick)
	if calls.Load() != 2 {
		t.Fatalf("ordinary active state was polled after its explicit wake: calls=%d", calls.Load())
	}
}

func TestFallbackSchedulerPermanentPolicyErrorWaitsForResourceChange(t *testing.T) {
	var calls atomic.Int64
	s, agent := schedulerLoopFixture(t, func(context.Context, string) error {
		calls.Add(1)
		return domain.Forbidden("synthetic policy needs correction")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eventually(t, ctx, func() bool { return calls.Load() == 1 })
	time.Sleep(3 * modelFallbackTick)
	if calls.Load() != 1 {
		t.Fatalf("permanent policy failure caused short retry polling: calls=%d", calls.Load())
	}
	agent.Name = "Corrected synthetic scheduler policy"
	if err := s.Store.Save(ctx, "agents", &agent.Meta, agent); err != nil {
		t.Fatal(err)
	}
	eventually(t, ctx, func() bool { return calls.Load() == 2 })
}

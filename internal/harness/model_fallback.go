package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	models "github.com/oines/astral-core/models"
	"github.com/oines/astral-core/models/provider"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
)

const modelFallbackKind = "model_fallback"

var (
	modelFallbackTick          = 250 * time.Millisecond
	modelFallbackProbeDelay    = 60 * time.Second
	modelFallbackProbeTimeout  = 4 * time.Minute
	errModelFallbackRunPending = errors.New("model fallback has not classified this terminal Run yet")
)

// This is Host operation state, not conversation history. The user's preferred
// selection is independent of the target currently serving an unfinished task.
type modelFallbackState struct {
	SessionID        string               `json:"session_id"`
	AgentID          string               `json:"agent_id"`
	Epoch            uint64               `json:"epoch"`
	Phase            string               `json:"phase"`
	Resumable        bool                 `json:"resumable,omitempty"`
	Preferred        core.ModelSelection  `json:"preferred"`
	SubagentBaseline *core.ModelSelection `json:"subagent_baseline,omitempty"`
	Active           core.ModelSelection  `json:"active"`
	Tried            []string             `json:"tried,omitempty"`
	FailureKind      string               `json:"failure_kind,omitempty"`
	FailureMessage   string               `json:"failure_message,omitempty"`
	FailedModelID    string               `json:"failed_model_id,omitempty"`
	FailedRunID      string               `json:"failed_run_id,omitempty"`
	ObservedRunID    string               `json:"observed_run_id,omitempty"`
	SuppressedRuns   []string             `json:"suppressed_runs,omitempty"`
	NextProbeAt      time.Time            `json:"next_probe_at,omitempty"`
}

type modelFallbackProbe struct {
	epoch  uint64
	cancel context.CancelFunc
}

// The scheduler retains only routing/deadline metadata, never history or the
// growing suppressed-Run list. Its queue lock is independent of operation locks;
// it is always released before reading Store or acquiring a runtime lock.
type modelFallbackScheduleState struct {
	id, agentID, phase, preferredID, activeID string
	nextProbeAt                               time.Time
}

type modelFallbackScheduleUpdate struct {
	state   modelFallbackScheduleState
	refresh bool
}

func fallbackScheduleState(state modelFallbackState) modelFallbackScheduleState {
	return modelFallbackScheduleState{id: state.SessionID, agentID: state.AgentID, phase: state.Phase,
		preferredID: state.Preferred.ID, activeID: state.Active.ID, nextProbeAt: state.NextProbeAt}
}

func (s *Service) queueModelFallback(update modelFallbackScheduleUpdate) {
	if update.state.id == "" || s.modelFallbackWake == nil {
		return
	}
	s.modelFallbackScheduleMu.Lock()
	// A later durable-state save may coalesce with a terminal wake, but must
	// not erase the explicit one-shot reconcile before the worker consumes it.
	update.refresh = update.refresh || s.modelFallbackUpdates[update.state.id].refresh
	s.modelFallbackUpdates[update.state.id] = update
	s.modelFallbackScheduleMu.Unlock()
	select {
	case s.modelFallbackWake <- struct{}{}:
	default:
	}
}

func (s *Service) wakeModelFallback(id string) {
	s.queueModelFallback(modelFallbackScheduleUpdate{state: modelFallbackScheduleState{id: id}, refresh: true})
}

func fallbackArmed(state modelFallbackState) bool {
	return state.Phase == "active" || state.Phase == "failed" || state.Phase == "switching" || state.Phase == "waiting"
}

// Lock order: callers may hold a runtime or subagent-tree lock before taking
// modelFallbackMu. No method takes either of those locks while holding it.
func (s *Service) saveModelFallback(ctx context.Context, state modelFallbackState) error {
	phase := state.Phase
	if !fallbackArmed(state) {
		phase = "complete"
	}
	if err := s.Store.SaveMaintenance(ctx, modelFallbackKind, state.SessionID, phase, state); err != nil {
		return err
	}
	// Fallback state changes concern this Session only. A coarse Store watch
	// would rescan every settled child whenever an unrelated root takes a turn.
	s.wakeSubagents(state.SessionID)
	s.wakeExternal(state.SessionID)
	s.queueModelFallback(modelFallbackScheduleUpdate{state: fallbackScheduleState(state)})
	return nil
}

func (s *Service) modelFallbackSelection(ctx context.Context, id string, baseline core.ModelSelection) (core.ModelSelection, error) {
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	if err := s.Store.Maintenance(ctx, modelFallbackKind, id, &state); err != nil {
		if domain.IsMissing(err) {
			return baseline, nil
		}
		return core.ModelSelection{}, err
	}
	if state.Active.ID != "" {
		return state.Active, nil
	}
	return baseline, nil
}

func (s *Service) modelPreferredSelection(ctx context.Context, id string, baseline core.ModelSelection) (core.ModelSelection, error) {
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	if err := s.Store.Maintenance(ctx, modelFallbackKind, id, &state); err != nil {
		if domain.IsMissing(err) {
			return baseline, nil
		}
		return core.ModelSelection{}, err
	}
	if state.Preferred.ID != "" {
		return state.Preferred, nil
	}
	return baseline, nil
}

// setPreferredModel changes intent without starting work. In particular, a
// bulk model update must not revive a stopped or exhausted task.
func (s *Service) setPreferredModel(ctx context.Context, id string, selection core.ModelSelection) error {
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	err := s.Store.Maintenance(ctx, modelFallbackKind, id, &state)
	if err != nil && !domain.IsMissing(err) {
		return err
	}
	if state.SessionID == "" {
		var session domain.Session
		if err := s.Store.Get(ctx, "sessions", id, &session); err != nil {
			return err
		}
		state = modelFallbackState{SessionID: id, AgentID: session.AgentID, Phase: "complete"}
	}
	baselineChanged := false
	var settings subagentSettings
	if settingsErr := s.Store.Get(ctx, "subagent_settings", id, &settings); settingsErr == nil {
		baselineChanged = state.SubagentBaseline == nil || !jsonEqual(*state.SubagentBaseline, settings.Configuration.Selection)
		baseline := settings.Configuration.Selection
		state.SubagentBaseline = &baseline
	} else if !domain.IsMissing(settingsErr) {
		return settingsErr
	}
	if jsonEqual(state.Preferred, selection) && jsonEqual(state.Active, selection) && len(state.Tried) == 0 && state.FailureKind == "" && state.Phase != "waiting" && state.Phase != "switching" && state.Phase != "failed" {
		if baselineChanged {
			return s.saveModelFallback(ctx, state)
		}
		return nil
	}
	state.Epoch++
	state.Preferred, state.Active = selection, selection
	state.Tried = nil
	state.FailureKind, state.FailureMessage, state.FailedModelID = "", "", ""
	state.NextProbeAt = time.Time{}
	if state.Phase == "switching" || state.Phase == "waiting" || state.Phase == "failed" {
		state.Phase = "switching"
	}
	if probe, ok := s.modelFallbackProbes[id]; ok {
		probe.cancel()
		delete(s.modelFallbackProbes, id)
	}
	return s.saveModelFallback(ctx, state)
}

// Saved child settings are a durable manual choice. Remember the last seen
// value separately from Active so a request does not undo an automatic fallback.
// The explicit setter snapshots saved settings before a bulk/tree writer replaces
// them, which also prevents the old saved choice from reverting accepted intent.
func (s *Service) modelFallbackSubagentSelection(ctx context.Context, id string, desired core.ModelSelection) (core.ModelSelection, error) {
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	err := s.Store.Maintenance(ctx, modelFallbackKind, id, &state)
	if err != nil && !domain.IsMissing(err) {
		return core.ModelSelection{}, err
	}
	if state.SessionID == "" {
		var session domain.Session
		if err := s.Store.Get(ctx, "sessions", id, &session); err != nil {
			return core.ModelSelection{}, err
		}
		state = modelFallbackState{SessionID: id, AgentID: session.AgentID, Preferred: desired, Active: desired, Phase: "complete"}
	}
	if state.SubagentBaseline == nil || !jsonEqual(*state.SubagentBaseline, desired) {
		if !jsonEqual(state.Preferred, desired) {
			state.Epoch++
			state.Preferred, state.Active, state.Tried = desired, desired, nil
			state.FailureKind, state.FailureMessage, state.FailedModelID = "", "", ""
			state.NextProbeAt = time.Time{}
			if state.Phase == "switching" || state.Phase == "waiting" || state.Phase == "failed" {
				state.Phase = "switching"
			}
			if probe, ok := s.modelFallbackProbes[id]; ok {
				probe.cancel()
				delete(s.modelFallbackProbes, id)
			}
		}
		baseline := desired
		state.SubagentBaseline = &baseline
		if err := s.saveModelFallback(ctx, state); err != nil {
			return core.ModelSelection{}, err
		}
	}
	return state.Active, nil
}

func (r *runtime) initializeModelFallback(ctx context.Context, preferred, active core.ModelSelection) error {
	s := r.service
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var previous modelFallbackState
	if err := s.Store.Maintenance(ctx, modelFallbackKind, r.id, &previous); err == nil {
		return nil
	} else if !domain.IsMissing(err) {
		return err
	}
	return s.saveModelFallback(ctx, modelFallbackState{SessionID: r.id, AgentID: r.agentID,
		Preferred: preferred, Active: active, Phase: "complete"})
}

// prepareModelFallback starts a logical task after durable input admission or
// an explicit Continue. Automatic recovery calls Core.Continue directly.
func (r *runtime) prepareModelFallback(ctx context.Context) error {
	snapshot, err := r.snapshot()
	if err != nil {
		return err
	}
	if snapshot.Running || snapshot.Maintenance {
		return nil
	}
	s := r.service
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	err = s.Store.Maintenance(ctx, modelFallbackKind, r.id, &state)
	if err != nil && !domain.IsMissing(err) {
		return err
	}
	if state.Preferred.ID == "" {
		state.Preferred = r.core.Load().Configuration().Selection
	}
	state.SessionID, state.AgentID = r.id, r.agentID
	state.Epoch++
	state.Phase, state.Active = "active", state.Preferred
	state.Resumable = false
	state.Tried = nil
	state.FailureKind, state.FailureMessage, state.FailedModelID, state.FailedRunID = "", "", "", ""
	state.ObservedRunID = ""
	state.NextProbeAt = time.Time{}
	if probe, ok := s.modelFallbackProbes[r.id]; ok {
		probe.cancel()
		delete(s.modelFallbackProbes, r.id)
	}
	return s.saveModelFallback(ctx, state)
}

func (s *Service) cancelModelFallback(ctx context.Context, id string) error {
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	err := s.Store.Maintenance(ctx, modelFallbackKind, id, &state)
	if err != nil && !domain.IsMissing(err) {
		return err
	}
	if probe, ok := s.modelFallbackProbes[id]; ok {
		probe.cancel()
		delete(s.modelFallbackProbes, id)
	}
	if state.SessionID == "" {
		return nil
	}
	state.Epoch++
	state.Phase = "cancelled"
	state.FailureKind, state.FailedRunID = "", ""
	state.NextProbeAt = time.Time{}
	return s.saveModelFallback(ctx, state)
}

func (s *Service) ModelFallbackPending(ctx context.Context, id string) (bool, error) {
	// Keep service shutdown from clearing the effective Core window while an
	// armed live handle supplies the authoritative queued-Run boundary.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, nil
	}
	live := s.runtimes[id]
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	if err := s.Store.Maintenance(ctx, modelFallbackKind, id, &state); err != nil {
		if domain.IsMissing(err) {
			return false, nil
		}
		return false, err
	}
	if state.Phase == "active" && state.ObservedRunID != "" {
		if live != nil && live.core.Load() != nil {
			var session domain.Session
			if err := s.Store.Get(ctx, "sessions", id, &session); err != nil || session.Deletion != nil {
				return false, err
			}
			snapshot, snapshotErr := live.snapshot()
			if snapshotErr != nil {
				return false, snapshotErr
			}
			if snapshot.Running || snapshot.Maintenance {
				return true, nil
			}
		}
		coreState, coreErr := s.CoreStore.LoadState(ctx, id)
		if coreErr != nil && !errors.Is(coreErr, core.ErrNotFound) {
			return false, coreErr
		}
		if coreState.LastRun == nil || coreState.LastRun.ID != state.ObservedRunID || coreState.LastRun.Status != "completed" {
			return true, nil
		}
		// New input may already have been accepted after natural completion.
		// Do not hold that input behind the reconciler's next polling interval.
		state.Phase = "complete"
		if probe, ok := s.modelFallbackProbes[id]; ok {
			probe.cancel()
			delete(s.modelFallbackProbes, id)
		}
		return false, s.saveModelFallback(ctx, state)
	}
	return fallbackArmed(state), nil
}

// A terminal failure is held until the fallback decision is durable. Only
// intermediate failed Runs are consumed silently by child-report delivery.
func (s *Service) suppressModelFallbackRun(ctx context.Context, id, runID string) (bool, error) {
	coreState, coreErr := s.CoreStore.LoadState(ctx, id)
	if coreErr != nil && !errors.Is(coreErr, core.ErrNotFound) {
		return false, coreErr
	}
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	if err := s.Store.Maintenance(ctx, modelFallbackKind, id, &state); err != nil {
		if domain.IsMissing(err) {
			return false, nil
		}
		return false, err
	}
	if slices.Contains(state.SuppressedRuns, runID) {
		return true, nil
	}
	terminalPending := fallbackArmed(state) && state.FailedRunID == runID ||
		state.Phase == "active" && coreState.LastRun != nil && coreState.LastRun.ID == runID && coreState.LastRun.Status == "failed"
	if terminalPending {
		// Final single/exhausted-model failures can be decided synchronously.
		// Reporting must not require a polling worker merely to release an
		// already-final terminal Run after a restart.
		var agent domain.Agent
		if err := s.Store.Get(ctx, "agents", state.AgentID, &agent); err != nil {
			return false, err
		}
		if !agent.ModelAutoRestore {
			failedID := state.FailedModelID
			if failedID == "" {
				attempts, err := s.CoreStore.ScanAttempts(ctx, id, core.AttemptQuery{Reverse: true, Limit: 1})
				if err != nil {
					return false, err
				}
				if len(attempts.Attempts) > 0 && attempts.Attempts[0].RunID == runID {
					failedID, _, _ = strings.Cut(attempts.Attempts[0].Input.Model.ID, "~")
				} else {
					failedID = state.Active.ID
				}
			}
			remaining := false
			for _, candidate := range modelFallbackCandidates(state.Preferred, agent) {
				if candidate.ID == failedID || slices.Contains(state.Tried, candidate.ID) {
					continue
				}
				available, _ := s.Models.Availability(ctx, candidate.ID)
				remaining = remaining || available
			}
			if !remaining {
				state.Phase = "exhausted"
				return false, s.saveModelFallback(ctx, state)
			}
		}
		return false, errModelFallbackRunPending
	}
	return false, nil
}

func fallbackErrorKind(err error) string {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, core.ErrOutcomeUnknown) {
		return ""
	}
	if errors.Is(err, core.ErrBudget) {
		return "context_budget"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request_timeout"
	}
	var classified *core.ModelError
	if errors.As(err, &classified) && classified.Code != "" {
		return classified.Code
	}
	var api *models.APIError
	if errors.As(err, &api) && api.Kind != "" {
		return string(api.Kind)
	}
	return "model_request_failed"
}

// The call boundary still owns typed errors. Persist their classification now;
// never infer retry/fallback policy by parsing a provider's display text later.
func (r *runtime) recordModelFallbackFailure(ctx context.Context, selection core.ModelSelection, failure error) error {
	if r == nil || r.service == nil || r.service.Store == nil {
		return nil
	}
	kind := fallbackErrorKind(failure)
	if kind == "" || r.service.ctx != nil && r.service.ctx.Err() != nil || r.core.Load() == nil {
		return nil
	}
	s := r.service
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	if err := s.Store.Maintenance(persist, modelFallbackKind, r.id, &state); err != nil {
		if domain.IsMissing(err) {
			return nil // inspection and passive creation do not start a task
		}
		return err
	}
	if !fallbackArmed(state) {
		return nil
	}
	// Use the short lifecycle gate inside model callbacks. Taking r.mu here
	// could deadlock with Close while it joins the executing model call.
	snapshot, snapshotErr := r.snapshot()
	if snapshotErr != nil || snapshot.Stopping {
		return nil
	}
	runID := snapshot.RunID
	if runID == "" && snapshot.State.LastRun != nil && snapshot.State.LastRun.Status == "failed" {
		runID = snapshot.State.LastRun.ID
	}
	state.Phase = "failed"
	state.FailureKind, state.FailureMessage = kind, failure.Error()
	state.FailedModelID, state.FailedRunID = selection.ID, runID
	if state.FailedModelID == "" {
		state.FailedModelID = state.Active.ID
	}
	if err := s.saveModelFallback(persist, state); err != nil {
		return err
	}
	s.notify(r.id)
	return nil
}

// A provider context-overflow response may be served by a later configured
// candidate. Preserve its diagnostic cause, but stop Core's same-model overflow
// repair in that case so the Host can advance the ordered policy once.
func (r *runtime) modelFallbackCallError(ctx context.Context, selection core.ModelSelection, failure error) error {
	recordErr := r.recordModelFallbackFailure(ctx, selection, failure)
	if r == nil || r.service == nil || r.service.Store == nil {
		return errors.Join(failure, recordErr)
	}
	var classified *core.ModelError
	if !errors.As(failure, &classified) || !classified.ContextOverflow {
		return errors.Join(failure, recordErr)
	}
	s := r.service
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	var agent domain.Agent
	if s.Store.Maintenance(context.WithoutCancel(ctx), modelFallbackKind, r.id, &state) != nil || !fallbackArmed(state) ||
		s.Store.Get(context.WithoutCancel(ctx), "agents", r.agentID, &agent) != nil {
		return errors.Join(failure, recordErr)
	}
	for _, candidate := range modelFallbackCandidates(state.Preferred, agent) {
		if candidate.ID != selection.ID && !slices.Contains(state.Tried, candidate.ID) {
			return &core.ModelError{Code: classified.Code, Message: failure.Error(), Cause: errors.Join(failure, recordErr)}
		}
	}
	return errors.Join(failure, recordErr)
}

// A successful intermediate model turn does not finish the logical task or
// forget its fallback cursor. Natural Run completion ends recovery/detection.
func (r *runtime) recordModelFallbackSuccess(ctx context.Context) error {
	if r == nil || r.service == nil || r.service.Store == nil {
		return nil
	}
	s := r.service
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	if err := s.Store.Maintenance(ctx, modelFallbackKind, r.id, &state); err != nil {
		if domain.IsMissing(err) {
			return nil
		}
		return err
	}
	if !fallbackArmed(state) {
		return nil
	}
	state.Phase = "active"
	state.FailureKind, state.FailureMessage, state.FailedModelID, state.FailedRunID = "", "", "", ""
	return s.saveModelFallback(ctx, state)
}

func (r *runtime) observeModelFallbackRun(run *core.Run) {
	if run == nil {
		return
	}
	s := r.service
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.modelFallbackWorkers.Add(1)
	s.mu.Unlock()
	observed := r.core.Load()
	s.modelFallbackMu.Lock()
	var armed modelFallbackState
	persist, persistCancel := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
	if s.Store.Maintenance(persist, modelFallbackKind, r.id, &armed) == nil && fallbackArmed(armed) {
		armed.ObservedRunID = run.ID()
		_ = s.saveModelFallback(persist, armed)
	}
	persistCancel()
	s.modelFallbackMu.Unlock()
	observedEpoch := armed.Epoch
	subscription := observed.Subscribe(256)
	go func() {
		defer s.modelFallbackWorkers.Done()
		defer subscription.Close()
		terminal := make(chan core.Event, 1)
		stopEvents, eventsDone := make(chan struct{}), make(chan struct{})
		// Drain streaming events immediately, including while Wait is blocked.
		// Only the latest terminal boundary is needed for following a Core-
		// created successor; Snapshot supplies the authoritative current Run.
		go func() {
			defer close(eventsDone)
			for {
				select {
				case <-s.ctx.Done():
					return
				case <-stopEvents:
					return
				case event, ok := <-subscription.C:
					if !ok {
						return
					}
					if event.Type != core.EventRunEnd {
						continue
					}
					s.wakeModelFallback(r.id)
					s.wakeExternal(r.id)
					select {
					case terminal <- event:
					default:
						select {
						case <-terminal:
						default:
						}
						select {
						case terminal <- event:
						default:
						}
					}
				}
			}
		}()
		defer func() { close(stopEvents); <-eventsDone }()
		_, err := run.Wait(s.ctx)
		if s.ctx.Err() != nil {
			return
		}
		// The model can report failure before Core leaves Running. Every terminal
		// Run, including validation/tool failures, supplies the later safe boundary.
		s.wakeModelFallback(r.id)
		s.wakeExternal(r.id)
		r.mu.Lock()
		var session domain.Session
		readErr := s.Store.Get(s.ctx, "sessions", r.id, &session)
		if readErr != nil || session.Deletion != nil || r.core.Load() != observed {
			r.mu.Unlock()
			return
		}
		s.modelFallbackMu.Lock()
		var state modelFallbackState
		readErr = s.Store.Maintenance(s.ctx, modelFallbackKind, r.id, &state)
		armed := readErr == nil && fallbackArmed(state) && state.Epoch == observedEpoch
		s.modelFallbackMu.Unlock()
		if !armed {
			r.mu.Unlock()
			return
		}
		snapshot, snapshotErr := r.snapshot()
		if snapshotErr != nil {
			r.mu.Unlock()
			return
		}
		if err != nil {
			if snapshot.Running || snapshot.State.LastRun == nil || snapshot.State.LastRun.ID != run.ID() {
				r.mu.Unlock()
				return
			}
			var classified *core.ModelError
			var api *models.APIError
			if errors.Is(err, core.ErrBudget) || errors.As(err, &classified) || errors.As(err, &api) {
				_ = r.recordModelFallbackFailure(s.ctx, r.core.Load().Configuration().Selection, err)
			}
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		// Core may start another Run for accepted queued input itself. Follow
		// the same handle until its logical work naturally ends, so an in-flight
		// preferred-model detector is cancelled even without an Owner View call.
		// A saturated/closed event subscription must not delay cancellation
		// until the independent SDK request reaches its deadline.
		follow := time.NewTicker(modelFallbackTick)
		defer follow.Stop()
		for {
			if s.finishModelFallbackIfCompleted(s.ctx, r, observed, observedEpoch) {
				return
			}
			select {
			case <-s.ctx.Done():
				return
			case <-follow.C:
				continue
			case event := <-terminal:
				if event.Type == core.EventRunEnd && event.Status != "completed" {
					if failure := modelFallbackInternalBudgetError(event.Error); failure != nil {
						s.modelFallbackMu.Lock()
						var current modelFallbackState
						valid := s.Store.Maintenance(s.ctx, modelFallbackKind, r.id, &current) == nil && current.Epoch == observedEpoch && fallbackArmed(current)
						s.modelFallbackMu.Unlock()
						if valid {
							_ = r.recordModelFallbackFailure(s.ctx, current.Active, failure)
						}
					}
					return
				}
			}
		}
	}()
}

func (s *Service) finishModelFallbackIfCompleted(ctx context.Context, r *runtime, observed *core.Session, epoch uint64) bool {
	if ctx.Err() != nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var session domain.Session
	if err := s.Store.Get(ctx, "sessions", r.id, &session); err != nil || session.Deletion != nil || r.core.Load() != observed {
		return true
	}
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	var state modelFallbackState
	if err := s.Store.Maintenance(ctx, modelFallbackKind, r.id, &state); err != nil || !fallbackArmed(state) || state.Epoch != epoch {
		return true
	}
	snapshot, snapshotErr := r.snapshot()
	if snapshotErr != nil {
		return true
	}
	if snapshot.Running || state.ObservedRunID == "" || snapshot.State.LastRun == nil || snapshot.State.LastRun.Status != "completed" {
		return false
	}
	state.Phase = "complete"
	if probe, ok := s.modelFallbackProbes[r.id]; ok {
		probe.cancel()
		delete(s.modelFallbackProbes, r.id)
	}
	_ = s.saveModelFallback(ctx, state)
	s.notify(r.id)
	return true
}

func (s *Service) modelFallbackDeadline(state modelFallbackScheduleState) time.Time {
	now := time.Now()
	switch state.phase {
	case "failed", "switching":
		return now
	case "active":
		if state.preferredID == state.activeID {
			return time.Time{}
		}
	case "waiting":
	default:
		return time.Time{}
	}
	var agent domain.Agent
	if err := s.Store.Get(s.ctx, "agents", state.agentID, &agent); err != nil {
		if domain.IsMissing(err) {
			return time.Time{}
		}
		return now.Add(modelFallbackTick)
	}
	if agent.Deletion {
		return time.Time{}
	}
	if !agent.ModelAutoRestore {
		if state.phase == "waiting" {
			return now // Persist exhausted promptly when the switch is disabled.
		}
		return time.Time{}
	}
	if state.nextProbeAt.IsZero() {
		return now
	}
	return state.nextProbeAt
}

func modelFallbackShortRetry(err error) bool {
	var configuration *domain.Error
	return err != nil && !errors.Is(err, context.Canceled) && !errors.As(err, &configuration)
}

func (s *Service) startModelFallbackWorker() {
	s.modelFallbackWorkers.Add(1)
	go s.runModelFallbackWorker(s.reconcileModelFallback)
}

func (s *Service) runModelFallbackWorker(reconcile func(context.Context, string) error) {
	defer s.modelFallbackWorkers.Done()
	// Operation saves notify their exact Session directly. Resource changes
	// can alter an armed task's policy; unrelated writes and settled history
	// must not cause another pending-operation scan.
	watcher := s.Store.Watch(persistence.ChangeKey{Kind: "agents"}, persistence.ChangeKey{Kind: "models"}, persistence.ChangeKey{Kind: "providers"})
	defer watcher.Close()
	changed := watcher.Changes() // Obtain before the initial restore/policy reads.
	type scheduled struct {
		due time.Time
	}
	type completed struct {
		id  string
		err error
	}
	entries := map[string]scheduled{}
	active := map[string]bool{}
	forced := map[string]bool{}
	done := make(chan completed, 256)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	set := func(state modelFallbackScheduleState, restore bool) {
		if state.phase != "active" && state.phase != "failed" && state.phase != "switching" && state.phase != "waiting" {
			delete(entries, state.id)
			delete(forced, state.id)
			return
		}
		due := s.modelFallbackDeadline(state)
		if restore && state.phase == "active" {
			// Inspect interrupted Runs once on restart; only the existing
			// Resumable business rule may Continue them.
			due = time.Now()
		}
		entries[state.id] = scheduled{due: due}
	}
	refresh := func(id string, force bool) {
		var state modelFallbackState
		err := s.Store.Maintenance(s.ctx, modelFallbackKind, id, &state)
		if domain.IsMissing(err) {
			delete(entries, id)
			delete(forced, id)
		} else if err != nil {
			entries[id] = scheduled{due: time.Now().Add(modelFallbackTick)}
		} else {
			set(fallbackScheduleState(state), false)
			if entry, ok := entries[id]; ok && force {
				forced[id] = true
				entry.due = time.Now()
				entries[id] = entry
			}
		}
	}
	drain := func() {
		s.modelFallbackScheduleMu.Lock()
		updates := s.modelFallbackUpdates
		s.modelFallbackUpdates = map[string]modelFallbackScheduleUpdate{}
		s.modelFallbackScheduleMu.Unlock()
		for id, update := range updates {
			if update.refresh {
				refresh(id, true)
			} else {
				set(update.state, false)
			}
		}
	}
	restored := false
	var restoreAt time.Time
	for {
		if s.ctx.Err() != nil {
			return
		}
		if !restored && !time.Now().Before(restoreAt) {
			items, err := s.Store.PendingMaintenance(s.ctx, modelFallbackKind)
			if err != nil {
				restoreAt = time.Now().Add(modelFallbackTick)
			} else {
				for _, raw := range items {
					var state modelFallbackState
					if json.Unmarshal(raw, &state) == nil && state.SessionID != "" {
						set(fallbackScheduleState(state), true)
					}
				}
				restored = true
			}
		}
		drain()
		now := time.Now()
		var next time.Time
		if !restored {
			next = restoreAt
		}
		for id, entry := range entries {
			if active[id] || entry.due.IsZero() {
				continue
			}
			if !entry.due.After(now) {
				active[id] = true
				delete(forced, id) // Later explicit wakes remain pending until done.
				s.modelFallbackWorkers.Add(1)
				go func() {
					defer s.modelFallbackWorkers.Done()
					err := reconcile(s.ctx, id)
					select {
					case done <- completed{id, err}:
					case <-s.ctx.Done():
					}
				}()
			} else if next.IsZero() || entry.due.Before(next) {
				next = entry.due
			}
		}
		var due <-chan time.Time
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if !next.IsZero() {
			timer.Reset(max(time.Until(next), 0))
			due = timer.C
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.modelFallbackWake:
		case <-due:
		case <-changed:
			changed = watcher.Changes()
			for id, entry := range entries {
				forced[id] = true
				entry.due = time.Now()
				entries[id] = entry
			}
		case result := <-done:
			delete(active, result.id)
			// A save made while this worker was active must be consumed before
			// its completion. Then reload the durable latest intent once.
			drain()
			refresh(result.id, false)
			if entry, ok := entries[result.id]; ok {
				if forced[result.id] {
					entry.due = time.Now()
					entries[result.id] = entry
				} else if result.err != nil && !modelFallbackShortRetry(result.err) {
					// Configuration/permission failures await a resource change or
					// explicit action; they are not transient storage/admission work.
					if !entry.due.After(time.Now()) {
						entry.due = time.Time{}
						entries[result.id] = entry
					}
				} else if modelFallbackShortRetry(result.err) || !entry.due.IsZero() && !entry.due.After(time.Now()) {
					// Core may still be unwinding a failed Run or in maintenance.
					// Retry only this unresolved target, never every armed task.
					entry.due = time.Now().Add(modelFallbackTick)
					entries[result.id] = entry
				}
			}
		}
	}
}

func modelFallbackCandidates(preferred core.ModelSelection, agent domain.Agent) []core.ModelSelection {
	result := []core.ModelSelection{}
	seen := map[string]bool{}
	for _, selection := range append([]core.ModelSelection{preferred}, AgentModelPriority(agent)...) {
		if selection.ID != "" && !seen[selection.ID] {
			seen[selection.ID] = true
			result = append(result, selection)
		}
	}
	return result
}

func (s *Service) reconcileModelFallback(ctx context.Context, id string) error {
	// Scheduler/terminal notifications are hints and can arrive after a probe
	// was postponed. Recheck the durable deadline before opening a cold Core
	// Session; a future waiting detector needs no execution/history window.
	var pending modelFallbackState
	if err := s.Store.Maintenance(ctx, modelFallbackKind, id, &pending); err == nil && pending.Phase == "waiting" && pending.NextProbeAt.After(time.Now()) {
		var agent domain.Agent
		if err := s.Store.Get(ctx, "agents", pending.AgentID, &agent); err == nil && agent.ModelAutoRestore && !agent.Deletion {
			return nil
		}
	}
	r, err := s.open(ctx, id)
	if err != nil {
		if domain.IsMissing(err) {
			return s.Store.DeleteMaintenance(ctx, modelFallbackKind, id)
		}
		return err
	}
	r.mu.Lock()
	s.modelFallbackMu.Lock()
	var state modelFallbackState
	if err = s.Store.Maintenance(ctx, modelFallbackKind, id, &state); err != nil || !fallbackArmed(state) {
		s.modelFallbackMu.Unlock()
		r.mu.Unlock()
		return err
	}
	var agent domain.Agent
	if err = s.Store.Get(ctx, "agents", r.agentID, &agent); domain.IsMissing(err) || err == nil && agent.Deletion {
		state.Epoch++
		state.Phase, state.NextProbeAt = "cancelled", time.Time{}
		if probe, ok := s.modelFallbackProbes[id]; ok {
			probe.cancel()
			delete(s.modelFallbackProbes, id)
		}
		err = s.saveModelFallback(ctx, state)
		s.modelFallbackMu.Unlock()
		r.mu.Unlock()
		s.notify(id)
		return err
	} else if err != nil {
		s.modelFallbackMu.Unlock()
		r.mu.Unlock()
		return err
	}
	snapshot, snapshotErr := r.snapshot()
	if snapshotErr != nil {
		s.modelFallbackMu.Unlock()
		r.mu.Unlock()
		return snapshotErr
	}
	if snapshot.Stopping {
		state.Phase = "cancelled"
		err = s.saveModelFallback(ctx, state)
		s.modelFallbackMu.Unlock()
		r.mu.Unlock()
		return err
	}
	if snapshot.Running || snapshot.Maintenance {
		probe := snapshot.Running && agent.ModelAutoRestore && state.Active.ID != state.Preferred.ID && !time.Now().Before(state.NextProbeAt)
		s.modelFallbackMu.Unlock()
		r.mu.Unlock()
		if probe {
			return s.probePreferredModel(ctx, r, state)
		}
		return nil
	}
	last := snapshot.State.LastRun
	if last != nil && last.Status == "completed" && state.ObservedRunID != "" && state.Phase != "failed" && state.Phase != "switching" && state.Phase != "waiting" {
		state.Phase = "complete"
		state.NextProbeAt = time.Time{}
		err = s.saveModelFallback(ctx, state)
		s.modelFallbackMu.Unlock()
		r.mu.Unlock()
		s.notify(id)
		return err
	}
	if !snapshot.CanContinue {
		state.Phase = "complete"
		err = s.saveModelFallback(ctx, state)
		s.modelFallbackMu.Unlock()
		r.mu.Unlock()
		s.notify(id)
		return err
	}
	if state.Phase == "waiting" {
		if !agent.ModelAutoRestore {
			state.Phase = "exhausted"
			err = s.saveModelFallback(ctx, state)
			s.modelFallbackMu.Unlock()
			r.mu.Unlock()
			s.notify(id)
			return err
		}
		due := !time.Now().Before(state.NextProbeAt)
		s.modelFallbackMu.Unlock()
		r.mu.Unlock()
		if due {
			return s.probePreferredModel(ctx, r, state)
		}
		return nil
	}
	if state.Phase == "active" && last != nil && last.Status == "failed" {
		if failure := modelFallbackInternalBudgetError(last.Error); failure != nil {
			state.Phase, state.FailureKind, state.FailureMessage = "failed", "context_budget", failure.Error()
			state.FailedRunID, state.FailedModelID = last.ID, state.Active.ID
		} else {
			attempts, attemptErr := r.core.Load().Attempts().Scan(ctx, core.AttemptQuery{Reverse: true, Limit: 1})
			if attemptErr != nil {
				s.modelFallbackMu.Unlock()
				r.mu.Unlock()
				return attemptErr
			}
			if len(attempts.Attempts) > 0 {
				attempt := attempts.Attempts[0]
				if attempt.RunID == last.ID && attempt.Outcome != nil && attempt.Outcome.Status == "failed" {
					state.Phase, state.FailureKind, state.FailureMessage = "failed", "model_response_failed", attempt.Outcome.Error
					state.FailedModelID, _, _ = strings.Cut(attempt.Input.Model.ID, "~")
					state.FailedRunID = last.ID
				}
			}
		}
	}
	if state.Phase == "active" {
		if (last == nil || last.Status == "started" || last.Status == "cancelled") && state.Resumable {
			// Only a task already recovering models resumes automatically.
			// Ordinary interrupted work keeps its pre-existing Core semantics.
			state.Phase = "switching"
		} else {
			// Tool/storage/protocol errors not observed at the model boundary
			// must not be turned into an automatic model retry.
			state.Phase = "complete"
			err = s.saveModelFallback(ctx, state)
			s.modelFallbackMu.Unlock()
			r.mu.Unlock()
			return err
		}
	}
	if state.Phase == "failed" {
		if state.FailedModelID != "" && !slices.Contains(state.Tried, state.FailedModelID) {
			state.Tried = append(state.Tried, state.FailedModelID)
		}
		found := false
		for _, candidate := range modelFallbackCandidates(state.Preferred, agent) {
			if slices.Contains(state.Tried, candidate.ID) {
				continue
			}
			if available, _ := s.Models.Availability(ctx, candidate.ID); !available {
				state.Tried = append(state.Tried, candidate.ID)
				continue
			}
			state.Active, state.Phase, state.Resumable = candidate, "switching", true
			if state.NextProbeAt.IsZero() {
				state.NextProbeAt = time.Now().Add(modelFallbackProbeDelay)
			}
			found = true
			break
		}
		if !found {
			state.Phase = "exhausted"
			if agent.ModelAutoRestore {
				state.Phase, state.Resumable = "waiting", true
				state.NextProbeAt = time.Now().Add(modelFallbackProbeDelay)
				if state.FailedRunID != "" && !slices.Contains(state.SuppressedRuns, state.FailedRunID) {
					state.SuppressedRuns = append(state.SuppressedRuns, state.FailedRunID)
				}
			}
			err = s.saveModelFallback(ctx, state)
			s.modelFallbackMu.Unlock()
			r.mu.Unlock()
			s.notify(id)
			return err
		}
	}
	if err = s.saveModelFallback(ctx, state); err != nil {
		s.modelFallbackMu.Unlock()
		r.mu.Unlock()
		return err
	}
	s.modelFallbackMu.Unlock()
	err = s.resumeModelFallbackLocked(ctx, r, state)
	r.mu.Unlock()
	return err
}

// r.mu serializes this boundary with Stop and manual model changes. The intent
// was saved first, so a crash after selection or Continue can be reconciled.
func (s *Service) resumeModelFallbackLocked(ctx context.Context, r *runtime, intent modelFallbackState) error {
	if err := r.core.Load().SelectModel(ctx, intent.Active); err != nil {
		return err
	}
	if err := r.syncBusinessConfig(ctx); err != nil {
		return r.recordModelFallbackFailure(ctx, intent.Active, err)
	}
	s.modelFallbackMu.Lock()
	var current modelFallbackState
	err := s.Store.Maintenance(ctx, modelFallbackKind, r.id, &current)
	if err != nil || current.Epoch != intent.Epoch || !fallbackArmed(current) {
		s.modelFallbackMu.Unlock()
		return err
	}
	// Continue starts its driver asynchronously. Keep model callbacks behind
	// this short operation lock until the successor identity is persisted.
	run, err := r.core.Load().Continue(ctx)
	if err != nil {
		s.modelFallbackMu.Unlock()
		if errors.Is(err, core.ErrBusy) {
			return nil
		}
		return err
	}
	if current.Epoch == intent.Epoch && fallbackArmed(current) {
		if intent.FailedRunID != "" && !slices.Contains(current.SuppressedRuns, intent.FailedRunID) {
			current.SuppressedRuns = append(current.SuppressedRuns, intent.FailedRunID)
		}
		current.Phase, current.Active, current.ObservedRunID = "active", intent.Active, run.ID()
		current.FailureKind, current.FailureMessage, current.FailedModelID, current.FailedRunID = "", "", "", ""
		err = s.saveModelFallback(ctx, current)
	}
	s.modelFallbackMu.Unlock()
	r.observeModelFallbackRun(run)
	s.notify(r.id)
	return err
}

// Detection is an independent SDK call against the preferred model's current
// configuration. It uses no tools, does not change conversation history, and
// never treats TCP connectivity or a cached model listing as recovery.
func (s *Service) probePreferredModel(ctx context.Context, r *runtime, intent modelFallbackState) error {
	probeCtx, cancel := context.WithTimeout(ctx, modelFallbackProbeTimeout)
	defer cancel()
	s.modelFallbackMu.Lock()
	var current modelFallbackState
	err := s.Store.Maintenance(ctx, modelFallbackKind, r.id, &current)
	if err != nil || current.Epoch != intent.Epoch || !fallbackArmed(current) || time.Now().Before(current.NextProbeAt) {
		s.modelFallbackMu.Unlock()
		return err
	}
	// The per-Session worker owns this call until it completes. This also
	// prevents a duplicate detector after a restart while its deadline passes.
	current.NextProbeAt = time.Now().Add(modelFallbackProbeTimeout + modelFallbackProbeDelay)
	if err = s.saveModelFallback(ctx, current); err != nil {
		s.modelFallbackMu.Unlock()
		return err
	}
	s.modelFallbackProbes[r.id] = modelFallbackProbe{epoch: intent.Epoch, cancel: cancel}
	s.modelFallbackMu.Unlock()
	resolved, probeErr := s.Models.ResolveSelection(modelbridge.WithSessionID(probeCtx, r.id), intent.Preferred)
	if probeErr == nil {
		request, buildErr := modelFallbackProbeRequest(resolved)
		probeErr = buildErr
		var reply core.Reply
		if probeErr == nil {
			reply, probeErr = provider.Call(probeCtx, resolved.Model, request, nil)
		}
		if probeErr == nil {
			probeErr = reply.Message.Validate()
			if probeErr == nil && (reply.Message.Role != core.RoleAssistant || len(reply.Message.Content) == 0 || reply.Message.FinishReason != core.FinishStop && reply.Message.FinishReason != core.FinishLength) {
				probeErr = core.ErrIncompleteResponse
			}
		}
	}
	// Current model/provider revisions and credential identities must still
	// match the successful real call; a configuration edit is not recovery.
	if probeErr == nil {
		latest, resolveErr := s.Models.ResolveSelection(modelbridge.WithSessionID(probeCtx, r.id), intent.Preferred)
		if resolveErr != nil || latest.Model.Spec() != resolved.Model.Spec() || !jsonEqual(latest.Snapshot, resolved.Snapshot) {
			probeErr = errors.New("preferred model changed during recovery detection")
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s.modelFallbackMu.Lock()
	defer s.modelFallbackMu.Unlock()
	if probe, ok := s.modelFallbackProbes[r.id]; ok && probe.epoch == intent.Epoch {
		delete(s.modelFallbackProbes, r.id)
	}
	if err = s.Store.Maintenance(ctx, modelFallbackKind, r.id, &current); err != nil {
		return err
	}
	if current.Epoch != intent.Epoch || !fallbackArmed(current) {
		return nil
	}
	current.NextProbeAt = time.Now().Add(modelFallbackProbeDelay)
	if err = s.saveModelFallback(ctx, current); err != nil {
		return err
	}
	if probeCtx.Err() != nil || probeErr != nil {
		return nil
	}
	var agent domain.Agent
	if err = s.Store.Get(ctx, "agents", r.agentID, &agent); err != nil || !agent.ModelAutoRestore || agent.Deletion {
		return err
	}
	if available, _ := s.Models.Availability(ctx, current.Preferred.ID); !available {
		return nil
	}
	snapshot, snapshotErr := r.snapshot()
	if snapshotErr != nil {
		return snapshotErr
	}
	if snapshot.Stopping || (!snapshot.Running && snapshot.State.LastRun != nil && snapshot.State.LastRun.Status == "completed") {
		return nil
	}
	if snapshot.Maintenance {
		return nil
	}
	// SelectModel only changes the successor of an already frozen request.
	if err = r.core.Load().SelectModel(ctx, current.Preferred); err != nil {
		return err
	}
	current.Active, current.Tried = current.Preferred, nil
	current.NextProbeAt = time.Time{}
	if snapshot.Running {
		current.Phase = "active"
		return s.saveModelFallback(ctx, current)
	}
	current.Phase = "switching"
	if err = s.saveModelFallback(ctx, current); err != nil {
		return err
	}
	// Release the operation lock before Continue, whose model preparation may
	// report another failure through the same Host operation state.
	s.modelFallbackMu.Unlock()
	err = s.resumeModelFallbackLocked(ctx, r, current)
	s.modelFallbackMu.Lock()
	return err
}

func modelFallbackProbeRequest(resolved core.ResolvedModel) (core.Request, error) {
	options := resolved.Options
	options.ToolChoice = core.ToolChoice{Mode: "none"}
	options.ResponseFormat, options.Stop = nil, nil
	spec := resolved.Model.Spec()
	output := min(64, spec.MaxOutputTokens)
	if options.Reasoning.Effort != "" || options.Reasoning.Enabled != nil && *options.Reasoning.Enabled {
		output = min(2048, spec.MaxOutputTokens)
	}
	if budget := options.Reasoning.BudgetTokens; budget != nil && *budget > 0 {
		if *budget >= spec.MaxOutputTokens {
			return core.Request{}, domain.Invalid("reasoning budget leaves no output space for recovery detection")
		}
		output = spec.MaxOutputTokens
		if *budget <= spec.MaxOutputTokens-64 {
			output = max(min(2048, spec.MaxOutputTokens), *budget+64)
		}
	}
	return core.Request{ModelID: spec.ID, ModelRevision: spec.Revision, SystemPrompt: "Reply with OK.",
		Messages: []core.Message{core.User("Check that generation is available.")}, Options: options, MaxOutputTokens: output}, nil
}

// Kept separate from provider messages for deterministic local budget errors
// emitted by a Core-created successor Run for which Host has no *Run handle.
func modelFallbackInternalBudgetError(text string) error {
	if strings.HasPrefix(text, core.ErrBudget.Error()) {
		return fmt.Errorf("%w: %s", core.ErrBudget, text)
	}
	return nil
}

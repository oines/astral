package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	sessions "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
)

type PendingInput struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Metadata messages.Metadata `json:"metadata"`
}
type View struct {
	domain.Session
	Selection              sessions.ModelSelection    `json:"selection"`
	ProfileID              string                     `json:"profile_id,omitempty"`
	Reasoning              *sessions.ReasoningOptions `json:"reasoning,omitempty"`
	RolloutID              string                     `json:"rollout_id"`
	HeadID                 string                     `json:"head_id"`
	Status                 string                     `json:"status"`
	CanContinue            bool                       `json:"can_continue"`
	EventSequence          uint64                     `json:"event_sequence"`
	Attempt                *sessions.AttemptStatus    `json:"attempt,omitempty"`
	RunID                  string                     `json:"run_id,omitempty"`
	Error                  string                     `json:"error,omitempty"`
	LastRun                *sessions.RunRecord        `json:"last_run,omitempty"`
	Pending                []PendingInput             `json:"pending"`
	Approvals              []sessions.PendingApproval `json:"approvals"`
	Unresolved             []sessions.ToolRecord      `json:"unresolved"`
	ContextUsage           *ContextUsage              `json:"context_usage,omitempty"`
	ModelAvailable         bool                       `json:"model_available"`
	ModelUnavailableReason string                     `json:"model_unavailable_reason,omitempty"`
}

// InspectContext describes persisted context without appending environment or
// configuration entries. Request preparation includes the current Bot persona.
func (s *Service) InspectContext(ctx context.Context, id string) (sessions.Context, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return sessions.Context{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.core.Load().InspectContext(context.WithValue(ctx, inspectContextKey{}, true))
}

func (s *Service) View(ctx context.Context, id string) (View, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return View{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, err := r.snapshotAfterInterruptedTools(ctx)
	if err != nil {
		return View{}, err
	}
	view := View{Status: "idle", CanContinue: snapshot.CanContinue, Pending: []PendingInput{}, Approvals: snapshot.Approvals, Unresolved: snapshot.Unresolved}
	view.EventSequence = snapshot.EventSequence
	if err = s.Store.Get(ctx, "sessions", id, &view.Session); err != nil {
		return view, err
	}
	view.Selection = r.core.Load().Configuration().Selection
	view.ProfileID, view.Reasoning = view.Selection.ID, view.Selection.Reasoning
	view.ModelAvailable, view.ModelUnavailableReason = s.Models.Availability(ctx, view.ProfileID)
	if !view.ModelAvailable && snapshot.State.CurrentAttemptID == "" {
		view.CanContinue = false
	}
	state := snapshot.State
	view.RolloutID, view.HeadID, view.LastRun = state.RolloutID, state.Branch.HeadID, state.LastRun
	if view.ContextUsage, err = r.reportedContextUsage(ctx, view.HeadID); err != nil {
		return view, err
	}
	if view.HeadID != "" {
		if stamp, readErr := s.sessionHeadTime(ctx, id, state.RolloutID, view.HeadID); readErr == nil && stamp.After(view.UpdatedAt) {
			view.UpdatedAt = stamp
		}
	}
	view.Error, view.Attempt = snapshot.Error, snapshot.Attempt
	if state.LastRun != nil && state.LastRun.Status == "failed" {
		view.Error = state.LastRun.Error
	}
	if snapshot.Running {
		view.Status, view.RunID, view.Error = "running", snapshot.RunID, ""
	}
	if snapshot.Maintenance {
		view.Status = "compacting"
	}
	if snapshot.Stopping {
		view.Status = "stopping"
	}
	resolved := len(view.Unresolved) == 0 && state.LastRun != nil && strings.HasPrefix(state.LastRun.Error, sessions.ErrOutcomeUnknown.Error())
	if resolved {
		view.Error = ""
	}
	pendingFallback, fallbackErr := s.ModelFallbackPending(ctx, id)
	if fallbackErr != nil {
		return view, fallbackErr
	}
	deliveryPaused := false
	if !snapshot.Running && !snapshot.Maintenance && (view.Error == "" || pendingFallback) {
		deliveryError, err := s.imDeliveryError(ctx, id)
		if err != nil {
			return view, err
		}
		deliveryPaused = deliveryError != ""
		if deliveryPaused {
			view.Error = deliveryError
		}
	}
	if deliveryPaused || len(view.Unresolved) > 0 || len(view.Approvals) > 0 || (!snapshot.Running && !snapshot.Maintenance && (!view.ModelAvailable || !resolved && (view.Error != "" || state.LastRun != nil && (state.LastRun.Status == "failed" || state.LastRun.Status == "started")))) {
		view.Status = "needs_attention"
	}
	if pendingFallback && !deliveryPaused && !snapshot.Stopping && !snapshot.Maintenance && len(view.Unresolved) == 0 && len(view.Approvals) == 0 {
		view.Status, view.Error, view.CanContinue = "running", "", false
		if view.RunID == "" && state.LastRun != nil {
			view.RunID = state.LastRun.ID
		}
	}
	pending, err := r.core.Load().PeekQueuedInputs(ctx)
	if err != nil {
		return view, err
	}
	for _, input := range pending {
		var metadata messages.Metadata
		if input.Message != nil {
			_ = json.Unmarshal(input.Message.Metadata, &metadata)
		}
		view.Pending = append(view.Pending, PendingInput{ID: input.ID, Kind: string(input.Kind), Metadata: metadata})
	}
	return view, nil
}

// ReadContext returns bounded active history without model resolution or preparation.
func (s *Service) ReadContext(ctx context.Context, id string) (sessions.Context, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return sessions.Context{}, err
	}
	return r.core.Load().ReadContext(ctx)
}
func (s *Service) ModelSelection(ctx context.Context, id string) (sessions.ModelSelection, error) {
	s.mu.Lock()
	r := s.runtimes[id]
	s.mu.Unlock()
	var state sessions.RuntimeState
	if r != nil {
		select {
		case <-ctx.Done():
			return sessions.ModelSelection{}, ctx.Err()
		case <-r.ready:
		}
		if r.openErr != nil {
			return sessions.ModelSelection{}, r.openErr
		}
		r.mu.Lock()
		state = r.core.Load().State()
		r.mu.Unlock()
	} else {
		var err error
		state, err = s.CoreStore.LoadState(ctx, id)
		if err != nil {
			return sessions.ModelSelection{}, err
		}
	}
	selection, err := s.selectionAtState(ctx, id, state)
	if err != nil {
		return sessions.ModelSelection{}, err
	}
	var value sessions.ModelSelection
	err = json.Unmarshal(selection.value, &value)
	return value, err
}

// PreferredModelSelection includes a saved manual choice while a different
// fallback target is active, and accepted children without a Core branch yet.
func (s *Service) PreferredModelSelection(ctx context.Context, id string) (sessions.ModelSelection, error) {
	selection, err := s.ModelSelection(ctx, id)
	if errors.Is(err, sessions.ErrNotFound) {
		var settings subagentSettings
		if readErr := s.Store.Get(ctx, "subagent_settings", id, &settings); readErr != nil {
			return sessions.ModelSelection{}, readErr
		}
		selection = settings.Configuration.Selection
	} else if err != nil {
		return sessions.ModelSelection{}, err
	}
	return s.modelPreferredSelection(ctx, id, selection)
}

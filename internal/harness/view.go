package harness

import (
	"context"
	"encoding/json"
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
	if err := r.finishInterruptedTools(ctx); err != nil {
		return View{}, err
	}
	snapshot := r.core.Load().Snapshot()
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
		if head, readErr := r.core.Load().History().Get(ctx, view.HeadID); readErr == nil && head.Time.After(view.UpdatedAt) {
			view.UpdatedAt = head.Time
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
	if len(view.Unresolved) > 0 || len(view.Approvals) > 0 || (!snapshot.Running && !snapshot.Maintenance && (!view.ModelAvailable || !resolved && (view.Error != "" || state.LastRun != nil && (state.LastRun.Status == "failed" || state.LastRun.Status == "started")))) {
		view.Status = "needs_attention"
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
	if r != nil {
		select {
		case <-ctx.Done():
			return sessions.ModelSelection{}, ctx.Err()
		case <-r.ready:
		}
		if r.openErr != nil {
			return sessions.ModelSelection{}, r.openErr
		}
		return r.core.Load().Configuration().Selection, nil
	}
	configuration, err := sessions.ReadConfiguration(ctx, s.CoreStore, id)
	return configuration.Selection, err
}

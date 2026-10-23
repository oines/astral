package harness

import (
	"context"
	"encoding/json"

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
	RolloutID     string                     `json:"rollout_id"`
	HeadID        string                     `json:"head_id"`
	Status        string                     `json:"status"`
	CanContinue   bool                       `json:"can_continue"`
	EventSequence uint64                     `json:"event_sequence"`
	Attempt       *sessions.AttemptStatus    `json:"attempt,omitempty"`
	RunID         string                     `json:"run_id,omitempty"`
	Error         string                     `json:"error,omitempty"`
	LastRun       *sessions.RunRecord        `json:"last_run,omitempty"`
	Pending       []PendingInput             `json:"pending"`
	Approvals     []sessions.PendingApproval `json:"approvals"`
	Unresolved    []sessions.ToolRecord      `json:"unresolved"`
	ContextUsage  *ContextUsage              `json:"context_usage,omitempty"`
}

// InspectContext describes persisted context without refreshing environment or
// configuration. The next activation reconciles any changed host settings.
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
	snapshot := r.core.Load().Snapshot()
	view := View{Status: "idle", CanContinue: snapshot.CanContinue, Pending: []PendingInput{}, Approvals: snapshot.Approvals, Unresolved: snapshot.Unresolved}
	view.EventSequence = snapshot.EventSequence
	if err = s.Store.Get(ctx, "sessions", id, &view.Session); err != nil {
		return view, err
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
	if len(view.Unresolved) > 0 || len(view.Approvals) > 0 || (!snapshot.Running && !snapshot.Maintenance && (view.Error != "" || state.LastRun != nil && (state.LastRun.Status == "failed" || state.LastRun.Status == "started"))) {
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

package harness

import (
	"context"
	"encoding/json"

	sessions "github.com/oines/astral-core/session"
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
	RolloutID  string                     `json:"rollout_id"`
	HeadID     string                     `json:"head_id"`
	Status     string                     `json:"status"`
	RunID      string                     `json:"run_id,omitempty"`
	Error      string                     `json:"error,omitempty"`
	LastRun    *sessions.RunRecord        `json:"last_run,omitempty"`
	Pending    []PendingInput             `json:"pending"`
	Approvals  []sessions.PendingApproval `json:"approvals"`
	Unresolved []sessions.ToolRecord      `json:"unresolved"`
}

func (s *Service) View(ctx context.Context, id string) (View, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return View{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	view := View{Status: "idle", Pending: []PendingInput{}, Approvals: r.core.PendingApprovals(), Unresolved: r.core.UnresolvedTools()}
	if err = s.Store.Get(ctx, "sessions", id, &view.Session); err != nil {
		return view, err
	}
	state := r.core.State()
	view.RolloutID, view.HeadID, view.LastRun = state.RolloutID, state.Branch.HeadID, state.LastRun
	view.Error = r.lastError
	if r.run != nil {
		view.Status, view.RunID = "running", r.run.ID()
	}
	if r.maintenance != "" {
		view.Status = r.maintenance
	}
	if r.stopping {
		view.Status = "stopping"
	}
	if len(view.Unresolved) > 0 || len(view.Approvals) > 0 || (r.run == nil && r.maintenance == "" && (view.Error != "" || state.LastRun != nil && (state.LastRun.Status == "failed" || state.LastRun.Status == "started"))) {
		view.Status = "needs_attention"
	}
	pending, err := r.core.PeekQueuedInputs(ctx)
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

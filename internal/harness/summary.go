package harness

import (
	"context"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

// SessionSummary is a directory entry, not a loaded execution view. Reading it
// must not open a Core Session, restore tools or load conversation history.
type SessionSummary struct {
	domain.Session
	Selection              core.ModelSelection    `json:"selection"`
	ProfileID              string                 `json:"profile_id,omitempty"`
	Reasoning              *core.ReasoningOptions `json:"reasoning,omitempty"`
	Status                 string                 `json:"status"`
	RunID                  string                 `json:"run_id,omitempty"`
	Error                  string                 `json:"error,omitempty"`
	ModelAvailable         bool                   `json:"model_available"`
	ModelUnavailableReason string                 `json:"model_unavailable_reason,omitempty"`
}

func (s *Service) storedSession(ctx context.Context, id string) (domain.Session, error) {
	var record domain.Session
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return record, core.ErrClosed
	}
	if err := s.Store.Get(ctx, "sessions", id, &record); err != nil {
		return record, err
	}
	if record.Deletion != nil {
		return record, domain.Missing("Session", id)
	}
	return record, s.agentAvailable(ctx, record.AgentID)
}

func (s *Service) Summary(ctx context.Context, id string) (SessionSummary, error) {
	record, err := s.storedSession(ctx, id)
	if err != nil {
		return SessionSummary{}, err
	}
	view := SessionSummary{Session: record, Status: "idle"}
	s.mu.Lock()
	r := s.runtimes[id]
	s.mu.Unlock()
	var state core.RuntimeState
	var snapshot *core.SessionSnapshot
	if r != nil {
		select {
		case <-r.ready:
			if r.openErr == nil {
				session := r.core.Load()
				value := session.Snapshot()
				if value.State.SessionID != "" {
					snapshot = &value
					state = value.State
				}
			}
		default:
		}
	}
	if snapshot == nil {
		state, err = s.CoreStore.LoadState(ctx, id)
		if err != nil {
			return view, err
		}
	}
	// Binding changes may replace the live Core handle. Read its durable
	// selection without calling Configuration on a concurrently closed handle.
	configuration, err := core.ReadConfiguration(ctx, s.CoreStore, id)
	if err != nil {
		return view, err
	}
	view.Selection = configuration.Selection
	if record.SubagentParentID != "" {
		var settings subagentSettings
		if err = s.Store.Get(ctx, "subagent_settings", id, &settings); err != nil {
			return view, err
		}
		view.Selection = settings.Configuration.Selection
	}
	if state.Branch.HeadID != "" {
		head, readErr := s.CoreStore.Get(ctx, state.RolloutID, state.Branch.HeadID)
		if readErr != nil {
			return view, readErr
		}
		if head.Entry.Time.After(view.UpdatedAt) {
			view.UpdatedAt = head.Entry.Time
		}
	}
	view.ProfileID, view.Reasoning = view.Selection.ID, view.Selection.Reasoning
	view.ModelAvailable, view.ModelUnavailableReason = s.Models.Availability(ctx, view.ProfileID)
	attention := len(state.Branch.Unresolved) > 0
	for execution, approval := range state.Approvals {
		if approval.Decision != nil {
			continue
		}
		for _, work := range state.Tools {
			if work.Prepared != nil && work.Prepared.Tool.Invocation.ExecutionID == execution && work.Result == nil && work.Control == nil && work.Unresolved == nil {
				attention = true
			}
		}
	}
	if state.LastRun != nil && state.LastRun.Status == "failed" {
		view.Error = state.LastRun.Error
	}
	if snapshot != nil {
		if view.Error == "" {
			view.Error = snapshot.Error
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
	}
	resolved := len(state.Branch.Unresolved) == 0 && state.LastRun != nil && strings.HasPrefix(state.LastRun.Error, core.ErrOutcomeUnknown.Error())
	if resolved {
		view.Error = ""
	}
	working := snapshot != nil && (snapshot.Running || snapshot.Maintenance)
	if !working && view.Error == "" {
		if view.Error, err = s.imDeliveryError(ctx, id); err != nil {
			return view, err
		}
		attention = attention || view.Error != ""
	}
	if attention || !working && (!view.ModelAvailable || !resolved && (view.Error != "" || state.LastRun != nil && (state.LastRun.Status == "failed" || state.LastRun.Status == "started"))) {
		view.Status = "needs_attention"
	}
	return view, nil
}

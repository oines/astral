package harness

import (
	"context"
	"strings"
	"time"

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
	var configuration core.Configuration
	if r != nil {
		select {
		case <-r.ready:
			if r.openErr == nil {
				r.mu.Lock()
				session := r.core.Load()
				value := session.Snapshot()
				if value.State.SessionID != "" {
					snapshot = &value
					state = value.State
					configuration = session.Configuration()
				}
				r.mu.Unlock()
			}
		default:
		}
	}
	if snapshot == nil {
		state, err = s.CoreStore.LoadState(ctx, id)
		if err != nil {
			return view, err
		}
		configuration, err = s.configurationAtState(ctx, id, state)
		if err != nil {
			return view, err
		}
	}
	view.Selection = configuration.Selection
	if state.Branch.HeadID != "" {
		headTime, readErr := s.sessionHeadTime(ctx, id, state.RolloutID, state.Branch.HeadID)
		if readErr != nil {
			return view, readErr
		}
		if headTime.After(view.UpdatedAt) {
			view.UpdatedAt = headTime
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

// Keep Core's configuration validation and error semantics while reusing the
// state already read for this cold directory entry. This adapter is local to
// the lookup; it does not cache state or configuration across requests.
type configurationStateStore struct {
	core.Store
	id    string
	state core.RuntimeState
}

func (s configurationStateStore) LoadState(ctx context.Context, id string) (core.RuntimeState, error) {
	if err := ctx.Err(); err != nil {
		return core.RuntimeState{}, err
	}
	if id == s.id {
		return s.state, nil
	}
	return s.Store.LoadState(ctx, id)
}

func (s *Service) configurationAtState(ctx context.Context, id string, state core.RuntimeState) (core.Configuration, error) {
	return core.ReadConfiguration(ctx, configurationStateStore{Store: s.CoreStore, id: id, state: state}, id)
}

type sessionHeadTime struct {
	head string
	time time.Time
}

// Core exposes complete records rather than a header-only query. Retain only
// one immutable head identity and timestamp per Session, discarding its body
// immediately so an unchanged directory does not decode that body again.
func (s *Service) sessionHeadTime(ctx context.Context, id, rollout, head string) (time.Time, error) {
	s.mu.Lock()
	cached, ok := s.headTimes[id]
	s.mu.Unlock()
	if ok && cached.head == head {
		return cached.time, nil
	}
	record, err := s.CoreStore.Get(ctx, rollout, head)
	if err != nil {
		return time.Time{}, err
	}
	stamp := record.Entry.Time
	s.mu.Lock()
	if s.headTimes == nil {
		s.headTimes = map[string]sessionHeadTime{}
	}
	s.headTimes[id] = sessionHeadTime{head: head, time: stamp}
	s.mu.Unlock()
	return stamp, nil
}

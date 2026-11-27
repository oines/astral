package harness

import (
	"context"
	"errors"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

// SubagentRunView is foreground state for an existing direct child. Reading it
// does not open the child's runtime or read its conversation.
type SubagentRunView struct {
	Title   string
	CWD     string
	Running bool
	LastRun *core.RunRecord
	active  bool
}

func (s *Service) ObserveSubagentRun(ctx context.Context, parentID, childID string) (SubagentRunView, error) {
	record, err := s.ownedSubagentRecord(ctx, parentID, childID, false)
	if err != nil {
		return SubagentRunView{}, err
	}
	view := SubagentRunView{Title: record.Title, CWD: record.CWD}
	s.mu.Lock()
	child := s.runtimes[record.ID]
	s.mu.Unlock()
	if child != nil {
		select {
		case <-child.ready:
			if child.openErr == nil {
				snapshot, err := child.snapshot()
				if err == nil {
					view.Running, view.LastRun = snapshot.Running, snapshot.State.LastRun
					view.active = snapshot.Running || snapshot.Maintenance || hasPendingSubagentWork(snapshot.State)
					return view, nil
				}
				if !errors.Is(err, core.ErrClosed) {
					return view, err
				}
			}
		default:
		}
	}
	state, err := s.CoreStore.LoadState(ctx, childID)
	if errors.Is(err, core.ErrNotFound) {
		return view, nil
	}
	view.LastRun = state.LastRun
	view.active = hasPendingSubagentWork(state)
	return view, err
}

func hasPendingSubagentWork(state core.RuntimeState) bool {
	return state.CurrentAttemptID != "" || state.LastRun != nil && state.LastRun.Status == "started" ||
		state.PendingInputs[core.InputSteer] != 0 || state.PendingInputs[core.InputFollowUp] != 0 || state.PendingInputs[core.InputContext] != 0
}

// ACPHasActiveSubagents lets ACP refuse to close an attachment while direct
// children have accepted or running work. It is a read of the current Host
// state and does not take ownership of those runs.
func (s *Service) ACPHasActiveSubagents(ctx context.Context, parentID string) (bool, error) {
	var parent domain.Session
	if err := s.Store.Get(ctx, "sessions", parentID, &parent); err != nil {
		return false, err
	}
	ids, err := s.Store.DirectSubagentIDs(ctx, parentID, parent.AgentID)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		child, err := s.ObserveSubagentRun(ctx, parentID, id)
		if domain.IsMissing(err) {
			continue
		}
		if err != nil || child.active {
			return child.active, err
		}
		if child.LastRun == nil {
			settings, err := s.childMetadata(ctx, id)
			if err == nil && !settings.started {
				return true, nil
			}
			if err != nil && !domain.IsMissing(err) {
				return false, err
			}
		}
	}
	return false, nil
}

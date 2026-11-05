package harness

import (
	"context"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const subagentActivityCursorKind = "subagent_activity_cursor"

type subagentActivityCursor struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Sequence  uint64 `json:"sequence"`
	Cursor    string `json:"cursor,omitempty"`
}

type subagentActivityRecord struct {
	domain.Meta
	AgentID  string                  `json:"agent_id"`
	Activity domain.SubagentActivity `json:"activity"`
}

func (s *Service) SubagentActivities(ctx context.Context, id, cursor string, limit int, reverse bool) (domain.SubagentActivityPage, error) {
	if _, err := s.storedSession(ctx, id); err != nil {
		return domain.SubagentActivityPage{}, err
	}
	return s.Store.SubagentActivities(ctx, id, cursor, limit, reverse)
}

// This projection is separate from model report delivery. A failed or paused
// parent must still show its child's actual lifecycle, including cancellation.
// Only Run facts are scanned; neither context nor assistant bodies are loaded.
func (s *Service) recordSubagentActivities(ctx context.Context, child domain.Session, state core.RuntimeState) (bool, error) {
	var parent domain.Session
	if err := s.Store.Get(ctx, "sessions", child.SubagentParentID, &parent); domain.IsMissing(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if parent.Deletion != nil {
		return false, nil
	}
	var cursor subagentActivityCursor
	if err := s.Store.Maintenance(ctx, subagentActivityCursorKind, child.ID, &cursor); err != nil && !domain.IsMissing(err) {
		return false, err
	}
	page, err := s.CoreStore.Scan(ctx, state.RolloutID, core.Query{SessionID: child.ID, Kind: core.EntryRun, Cursor: cursor.Cursor, Limit: 64, MaxBytes: 128 << 20})
	if err != nil {
		return false, err
	}
	for _, record := range page.Records {
		if record.Entry.Sequence <= cursor.Sequence {
			continue
		}
		if err := s.recordSubagentActivity(ctx, child, record.Entry); err != nil {
			return false, err
		}
	}
	if page.Next != "" {
		if page.Next == cursor.Cursor {
			return false, core.ErrCorrupt
		}
		if err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
			var current subagentActivityCursor
			if err := tx.Maintenance(ctx, subagentActivityCursorKind, child.ID, &current); err != nil && !domain.IsMissing(err) {
				return err
			}
			current.AgentID, current.SessionID, current.Cursor = child.AgentID, child.SubagentParentID, page.Next
			return tx.SaveMaintenance(ctx, subagentActivityCursorKind, child.ID, "complete", current)
		}); err != nil {
			return false, err
		}
	}
	// Non-Run commits need no checkpoint write. Only actual lifecycle facts
	// advance this small marker; idle/configuration reads never create I/O.
	return page.Next != "", nil
}

func (s *Service) recordSubagentActivity(ctx context.Context, child domain.Session, entry core.Entry) error {
	if child.SubagentParentID == "" || entry.Run == nil || entry.ID == "" {
		return nil
	}
	switch entry.Run.Status {
	case "started", "completed", "failed", "cancelled":
	default:
		return nil
	}
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var parent domain.Session
		if err := tx.Get(ctx, "sessions", child.SubagentParentID, &parent); domain.IsMissing(err) {
			return nil
		} else if err != nil {
			return err
		}
		if parent.Deletion != nil {
			return nil
		}
		var cursor subagentActivityCursor
		if err := tx.Maintenance(ctx, subagentActivityCursorKind, child.ID, &cursor); err != nil && !domain.IsMissing(err) {
			return err
		}
		if entry.Sequence <= cursor.Sequence {
			return nil
		}
		var journal subagentActivityRecord
		if err := tx.Get(ctx, persistence.SubagentActivityKind, parent.ID, &journal); err != nil && !domain.IsMissing(err) {
			return err
		}
		journal.ID, journal.AgentID = parent.ID, parent.AgentID
		journal.Activity = domain.SubagentActivity{
			ID: child.ID + ":" + entry.ID, SessionID: parent.ID, ChildSessionID: child.ID,
			Name: child.Title, RunID: entry.Run.ID, Status: entry.Run.Status,
			Time: entry.Time, Sequence: uint64(journal.Revision + 1),
		}
		if err := tx.Save(ctx, persistence.SubagentActivityKind, &journal.Meta, &journal); err != nil {
			return err
		}
		cursor.AgentID, cursor.SessionID, cursor.Sequence = child.AgentID, parent.ID, entry.Sequence
		return tx.SaveMaintenance(ctx, subagentActivityCursorKind, child.ID, "complete", cursor)
	})
}

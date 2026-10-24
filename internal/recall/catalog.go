package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
)

func activityTime(e core.Entry) int64 {
	if e.Tool != nil && e.Tool.Result != nil {
		return e.Time.UnixNano()
	}
	if m := e.Message; m != nil && !m.Failed() && (m.Role == core.RoleUser || m.Role == core.RoleAssistant) &&
		m.ContextKind != "astral.environment" && m.ContextKind != "im.trigger" {
		var metadata messages.Metadata
		_ = json.Unmarshal(m.Metadata, &metadata)
		if metadata.Source != "im.trigger" {
			return e.Time.UnixNano()
		}
	}
	return 0
}

// Listing needs only the last substantive entry, not a full-text backfill.
// Cache heads independently so repeated catalog requests do not reread content.
func (s *Service) lastActivity(ctx context.Context, db *sql.DB, rollout, head string) (int64, error) {
	if head == "" {
		return 0, nil
	}
	var activity int64
	seen := map[string]bool{}
	for id := head; id != ""; {
		if seen[id] {
			return 0, core.ErrCorrupt
		}
		seen[id] = true
		err := db.QueryRowContext(ctx, `SELECT activity_at FROM entries WHERE rollout_id=? AND id=?
 UNION ALL SELECT activity_at FROM catalog_heads WHERE rollout_id=? AND head_id=? LIMIT 1`, rollout, id, rollout, id).Scan(&activity)
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		record, err := s.core.Get(ctx, rollout, id)
		if err != nil {
			return 0, err
		}
		if record.Projection == nil || record.Entry.ID != id {
			return 0, core.ErrCorrupt
		}
		activity = activityTime(record.Entry)
		if activity != 0 {
			break
		}
		id = record.Entry.ParentID
	}
	_, err := db.ExecContext(ctx, "INSERT OR IGNORE INTO catalog_heads VALUES(?,?,?)", rollout, head, activity)
	return activity, err
}

func (s *Service) catalog(ctx context.Context, agentID string, ids []string) ([]domain.Session, error) {
	var sessions []domain.Session
	if len(ids) > 0 {
		for _, id := range ids {
			session, err := s.authorize(ctx, agentID, id)
			if err != nil {
				return nil, err
			}
			sessions = append(sessions, session)
		}
		return sessions, nil
	}
	records, err := s.store.List(ctx, "sessions")
	if err != nil {
		return nil, err
	}
	for _, raw := range records {
		var session domain.Session
		if err = json.Unmarshal(raw.Data, &session); err != nil {
			return nil, err
		}
		if session.AgentID == agentID && session.Deletion == nil {
			sessions = append(sessions, session)
		}
	}
	return sessions, nil
}

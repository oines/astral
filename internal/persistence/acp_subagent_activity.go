package persistence

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/oines/astral/internal/domain"
)

// SubagentActivityHeads returns one latest journal fact per direct child and
// the corresponding cursor from the same SQLite read. Resume only needs these
// identities and this cursor, never a replay of the earlier transitions.
func (s *Store) SubagentActivityHeads(ctx context.Context, parent string) (domain.SubagentActivityPage, error) {
	page := domain.SubagentActivityPage{Cursor: "0"}
	rows, err := s.db.QueryContext(ctx, `
WITH latest AS (
	SELECT MAX(revision) AS revision
	FROM versions
	WHERE kind = ? AND id = ?
	GROUP BY json_extract(data, '$.activity.child_session_id')
)
SELECT v.revision, v.data FROM latest
JOIN versions v ON v.kind = ? AND v.id = ? AND v.revision = latest.revision
ORDER BY v.revision`, SubagentActivityKind, parent, SubagentActivityKind, parent)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var revision uint64
		var raw []byte
		if err = rows.Scan(&revision, &raw); err != nil {
			return page, err
		}
		var record struct {
			Activity domain.SubagentActivity `json:"activity"`
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			return page, err
		}
		page.Activities = append(page.Activities, record.Activity)
		page.Cursor = strconv.FormatUint(revision, 10)
	}
	return page, rows.Err()
}

// DirectSubagentIDs reads the current association records, including children
// which have been registered but have not yet written a Run to the journal.
func (s *Store) DirectSubagentIDs(ctx context.Context, parent, botID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id FROM documents
WHERE kind = 'sessions'
  AND json_extract(data, '$.subagent_parent_id') = ?
  AND json_extract(data, '$.agent_id') = ?
  AND COALESCE(json_extract(data, '$.archived'), 0) = 0
  AND json_extract(data, '$.deletion') IS NULL`, parent, botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

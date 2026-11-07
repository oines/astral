package persistence

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/oines/astral/internal/domain"
)

const SubagentActivityKind = "subagent_activity"

// A journal belongs to its parent. Deleting just a child keeps those facts;
// deleting the parent removes both the current record and its revisions.
func (tx *Tx) PurgeSubagentActivities(ctx context.Context, parent string) error {
	for _, table := range []string{"documents", "versions"} {
		if _, err := tx.execKey(ctx, ChangeKey{Kind: SubagentActivityKind, ID: parent}, "DELETE FROM "+table+" WHERE kind=? AND id=?", SubagentActivityKind, parent); err != nil {
			return err
		}
	}
	return nil
}

// SubagentActivities reads the existing revision journal by its indexed parent
// ID and append sequence. Source timestamps never serve as delivery cursors:
// an older child event can arrive after a newer event from another child.
func (s *Store) SubagentActivities(ctx context.Context, parent, cursor string, limit int, reverse bool) (domain.SubagentActivityPage, error) {
	page := domain.SubagentActivityPage{Activities: []domain.SubagentActivity{}, Cursor: "0"}
	var boundary uint64
	if cursor != "" {
		var err error
		boundary, err = strconv.ParseUint(cursor, 10, 63)
		if err != nil {
			return page, domain.Invalid("invalid subagent activity cursor")
		}
	}
	limit = max(1, min(limit, 100))
	query := "SELECT data FROM versions WHERE kind=? AND id=?"
	args := []any{SubagentActivityKind, parent}
	if reverse {
		if cursor != "" {
			query += " AND revision<?"
			args = append(args, boundary)
		}
		query += " ORDER BY revision DESC LIMIT ?"
	} else {
		query += " AND revision>? ORDER BY revision ASC LIMIT ?"
		args = append(args, boundary)
		page.Cursor = strconv.FormatUint(boundary, 10)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return page, err
		}
		var record struct {
			Activity domain.SubagentActivity `json:"activity"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return page, err
		}
		if len(page.Activities) == limit {
			page.NextCursor = strconv.FormatUint(page.Activities[len(page.Activities)-1].Sequence, 10)
			break
		}
		page.Activities = append(page.Activities, record.Activity)
		if !reverse || len(page.Activities) == 1 {
			page.Cursor = strconv.FormatUint(record.Activity.Sequence, 10)
		}
	}
	return page, rows.Err()
}

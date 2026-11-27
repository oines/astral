package persistence

import "context"

// BotSessionRecords loads only this Bot's non-private root sessions, or the
// direct children of a parent already authorized by the caller.
func (s *Store) BotSessionRecords(ctx context.Context, botID, parentID string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, revision, data FROM documents
WHERE kind = 'sessions' AND json_extract(data, '$.agent_id') = ?
  AND COALESCE(json_extract(data, '$.subagent_parent_id'), '') = ?
  AND (? <> '' OR COALESCE(json_extract(data, '$.private'), 0) = 0)
  AND json_extract(data, '$.deletion') IS NULL
ORDER BY id`, botID, parentID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []Record{}
	for rows.Next() {
		var record Record
		if err := rows.Scan(&record.ID, &record.Revision, &record.Data); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// SessionDirectoryEntry contains the stored fields needed to discover a session.
type SessionDirectoryEntry struct {
	ID    string
	CWD   string
	Title string
}

// ListVisibleBotSessions reads up to limit visible non-subagent sessions with
// a stored working directory. The caller can continue from the last ID even
// if that session has since been deleted.
func (s *Store) ListVisibleBotSessions(ctx context.Context, botID, cwd, afterID string, limit int) ([]SessionDirectoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, json_extract(data, '$.cwd'), COALESCE(json_extract(data, '$.title'), '')
FROM documents
WHERE kind = 'sessions' AND id > ? AND json_extract(data, '$.agent_id') = ?
  AND COALESCE(json_extract(data, '$.subagent_parent_id'), '') = ''
  AND COALESCE(json_extract(data, '$.archived'), 0) = 0
  AND COALESCE(json_extract(data, '$.private'), 0) = 0
  AND json_extract(data, '$.deletion') IS NULL
  AND json_extract(data, '$.cwd') <> ''
  AND (? = '' OR json_extract(data, '$.cwd') = ?)
ORDER BY id
LIMIT ?`, afterID, botID, cwd, cwd, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]SessionDirectoryEntry, 0, limit)
	for rows.Next() {
		var entry SessionDirectoryEntry
		if err := rows.Scan(&entry.ID, &entry.CWD, &entry.Title); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

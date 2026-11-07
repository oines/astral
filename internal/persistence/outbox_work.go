package persistence

import "context"

// OutboxWork excludes confirmed history before copying payloads into Go.
// All other statuses remain available to the existing scheduler's ordering
// and dependency rules, including uncertain effects that must not be retried.
func (s *Store) OutboxWork(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,revision,data FROM documents WHERE kind='outbox' AND json_extract(data,'$.status') IS NOT 'accepted' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var record Record
		if err = rows.Scan(&record.ID, &record.Revision, &record.Data); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

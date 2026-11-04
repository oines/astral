package persistence

import "context"

// Only pending bodies are returned. Window ordering works on small routing
// metadata; accepted history is never decoded or sorted by the scheduler.
const outboxDispatchQuery = `
WITH eligible AS (
 SELECT p.id,
        substr(replace(CASE WHEN substr(json_extract(p.data,'$.created_at'),20,1)='Z'
          THEN substr(json_extract(p.data,'$.created_at'),1,19)||'.000000000Z'
          ELSE json_extract(p.data,'$.created_at') END,'Z','000000000'),1,29) AS created_at,
        json_extract(p.data,'$.group_id') AS group_id,
        coalesce(json_extract(p.data,'$.fragment'),0) AS fragment,
        coalesce(json_extract(p.data,'$.binding_id'),'') AS binding_id,
        json_extract(p.data,'$.conversation_id') AS conversation_id
 FROM documents p
 WHERE p.kind='outbox' AND json_extract(p.data,'$.status')='pending'
 AND NOT EXISTS (
   SELECT 1 FROM documents prior
   WHERE prior.kind='outbox'
     AND json_extract(prior.data,'$.group_id')=json_extract(p.data,'$.group_id')
     AND json_extract(prior.data,'$.status')='pending'
     AND coalesce(json_extract(prior.data,'$.fragment'),0)<coalesce(json_extract(p.data,'$.fragment'),0)
 )
 AND NOT EXISTS (
   SELECT 1 FROM documents failed
   WHERE failed.kind='outbox'
     AND json_extract(failed.data,'$.group_id')=json_extract(p.data,'$.group_id')
     AND json_extract(failed.data,'$.status') IN ('failed','blocked','unknown')
     AND coalesce(json_extract(failed.data,'$.fragment'),0)<coalesce(json_extract(p.data,'$.fragment'),0)
 )
 AND NOT EXISTS (
   SELECT 1 FROM documents active
   WHERE active.kind='outbox'
     AND json_extract(active.data,'$.binding_id') IS json_extract(p.data,'$.binding_id')
     AND json_extract(active.data,'$.conversation_id')=json_extract(p.data,'$.conversation_id')
     AND json_extract(active.data,'$.status')='dispatching'
 )
), heads AS (
 SELECT id,created_at,group_id,fragment,
   row_number() OVER (PARTITION BY binding_id,conversation_id ORDER BY created_at,group_id,fragment,id) AS position
 FROM eligible
)
SELECT d.id,d.revision,d.data FROM heads h
JOIN documents d ON d.kind='outbox' AND d.id=h.id
WHERE h.position=1 ORDER BY h.created_at,h.group_id,h.fragment,h.id LIMIT ?`

func (s *Store) OutboxDispatchCandidates(ctx context.Context, limit int) ([]Record, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, outboxDispatchQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Record
	for rows.Next() {
		var r Record
		if err = rows.Scan(&r.ID, &r.Revision, &r.Data); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

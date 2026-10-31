package persistence

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oines/astral/internal/domain"
)

// PrunedObservation keeps only event identity and sequence. It is a receipt,
// never an archived message, and prevents a platform retry from reinjecting it.
const PrunedObservation = "archive.pruned"

type ArchiveInfo struct {
	Count         int64  `json:"count"`
	Bytes         int64  `json:"bytes"`
	Oldest        string `json:"oldest,omitempty"`
	Newest        string `json:"newest,omitempty"`
	RetentionDays int    `json:"retention_days"`
}

func (s *Store) ConversationStorageBytes(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT conversation_id,COALESCE(SUM(length(payload)),0) FROM observations WHERE COALESCE(json_extract(payload,'$.kind'),'')<>? GROUP BY conversation_id`, PrunedObservation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]int64{}
	for rows.Next() {
		var id string
		var size int64
		if err = rows.Scan(&id, &size); err != nil {
			return nil, err
		}
		result[id] = size
	}
	return result, rows.Err()
}

func (s *Store) ConversationArchiveInfo(ctx context.Context, id string) (ArchiveInfo, error) {
	var result ArchiveInfo
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(payload)),0),COALESCE(MIN(json_extract(payload,'$.received_at')),''),COALESCE(MAX(json_extract(payload,'$.received_at')),'') FROM observations WHERE conversation_id=? AND COALESCE(json_extract(payload,'$.kind'),'')<>?`, id, PrunedObservation).Scan(&result.Count, &result.Bytes, &result.Oldest, &result.Newest)
	if err != nil {
		return result, err
	}
	var policy struct {
		RetentionDays int `json:"retention_days"`
	}
	if err = s.Get(ctx, "storage_retention", id, &policy); err == nil {
		result.RetentionDays = policy.RetentionDays
	} else if !domain.IsMissing(err) {
		return result, err
	}
	return result, nil
}

// PruneObservations bounds one write transaction and preserves the archive head.
// The caller persists the returned sequence with its progress in apply. Records
// after the processed watermark cannot be pruned. On error the transaction is
// rolled back, so neither the returned count nor cursor may be committed.
func (s *Store) PruneObservations(ctx context.Context, conversation string, after, through int64, before time.Time, apply func(*Tx, int, int64) error) (int, int64, error) {
	count := 0
	next := after
	err := s.Update(ctx, func(tx *Tx) error {
		rows, err := tx.tx.QueryContext(ctx, `SELECT sequence,payload FROM observations WHERE conversation_id=? AND sequence>? AND sequence<=? AND COALESCE(json_extract(payload,'$.kind'),'')<>? AND julianday(COALESCE(json_extract(payload,'$.received_at'),json_extract(payload,'$.sent_at'))) < julianday(?) ORDER BY sequence LIMIT 256`, conversation, after, through, PrunedObservation, before.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		type item struct {
			seq   int64
			event domain.Observation
		}
		var items []item
		for rows.Next() {
			var it item
			var raw []byte
			if err = rows.Scan(&it.seq, &raw); err != nil {
				rows.Close()
				return err
			}
			if err = json.Unmarshal(raw, &it.event); err != nil {
				rows.Close()
				return err
			}
			items = append(items, it)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err = rows.Close(); err != nil {
			return err
		}
		for _, it := range items {
			receipt := struct {
				ID              string `json:"id"`
				ConnectionID    string `json:"connection_id"`
				ConversationID  string `json:"conversation_id"`
				ExternalEventID string `json:"external_event_id"`
				Kind            string `json:"kind"`
			}{it.event.ID, it.event.ConnectionID, it.event.ConversationID, it.event.ExternalEventID, PrunedObservation}
			raw, err := json.Marshal(receipt)
			if err != nil {
				return err
			}
			if _, err = tx.tx.ExecContext(ctx, "UPDATE observations SET payload=? WHERE sequence=? AND conversation_id=?", raw, it.seq, conversation); err != nil {
				return err
			}
			count++
			next = it.seq
		}
		if apply != nil {
			return apply(tx, count, next)
		}
		return nil
	})
	return count, next, err
}

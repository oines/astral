package persistence

import "context"

// PruneWakeReceipts reclaims bounded-age idempotency metadata, not event bodies.
func (s *Store) PruneWakeReceipts(ctx context.Context, now int64) error {
	_, err := s.execKey(ctx, ChangeKey{Kind: "wake_receipt"}, "DELETE FROM operations WHERE kind='wake_receipt' AND CAST(json_extract(data,'$.expires_at') AS INTEGER)<=?", now)
	return err
}

func (tx *Tx) purgeSessionWake(ctx context.Context, id string) error {
	for _, table := range []string{"documents", "versions"} {
		if _, err := tx.exec(ctx, "DELETE FROM "+table+" WHERE kind='wake_endpoints' AND json_extract(data,'$.session_id')=?", id); err != nil {
			return err
		}
	}
	_, err := tx.exec(ctx, "DELETE FROM operations WHERE kind IN ('wake_buffer','wake_status','wake_receipt') AND json_extract(data,'$.session_id')=?", id)
	return err
}

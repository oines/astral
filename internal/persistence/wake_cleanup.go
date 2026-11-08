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

// PurgeComputerWakeEndpoints permanently removes a Computer's endpoint
// configuration, including revisions, and its replaceable receiving status.
// Accepted event receipts retain their existing expiry; flow filtering belongs
// to the wake controller under the target Sessions' admission locks.
func (tx *Tx) PurgeComputerWakeEndpoints(ctx context.Context, computerID string) error {
	if _, err := tx.execKey(ctx, ChangeKey{Kind: "wake_status"}, `DELETE FROM operations WHERE kind='wake_status' AND json_extract(data,'$.endpoint_id') IN (
		SELECT id FROM documents WHERE kind='wake_endpoints' AND json_extract(data,'$.computer_id')=?
		UNION SELECT id FROM versions WHERE kind='wake_endpoints' AND json_extract(data,'$.computer_id')=?)`, computerID, computerID); err != nil {
		return err
	}
	for _, table := range []string{"documents", "versions"} {
		if _, err := tx.execKey(ctx, ChangeKey{Kind: "wake_endpoints"}, "DELETE FROM "+table+" WHERE kind='wake_endpoints' AND json_extract(data,'$.computer_id')=?", computerID); err != nil {
			return err
		}
	}
	return nil
}

package recall

import "context"

// DeleteSession erases its private Rollout projection and invalidates snapshot
// cursors containing that Session, rather than retaining deleted passages.
func (s *Service) DeleteSession(ctx context.Context, id, rollout string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.database(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM snapshots WHERE id IN (SELECT snapshot_id FROM snapshot_sessions WHERE session_id=?)", id); err != nil {
		return err
	}
	if rollout != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO blocks_fts(blocks_fts,rowid,folded) SELECT 'delete',id,folded FROM blocks WHERE rollout_id=?", rollout); err != nil {
			return err
		}
		for _, table := range []string{"blocks", "entries", "catalog_heads"} {
			if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rollout_id=?", rollout); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

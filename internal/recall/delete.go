package recall

import (
	"context"

	"github.com/oines/astral/internal/domain"
)

// DeleteAgent completes the snapshot cleanup after DeleteSession has erased
// each owned Rollout projection. Empty-result snapshots have no Session link,
// so they must be removed by Agent ownership as well. The durable Host intent
// and the same mutex used by snapshot/index creation prevent late rebuilds.
func (s *Service) DeleteAgent(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var agent domain.Agent
	if err := s.store.Get(ctx, "agents", id, &agent); err != nil {
		if !domain.IsMissing(err) {
			return err
		}
	} else if !agent.Deletion {
		return domain.Conflict("Bot deletion intent is missing")
	}
	db, err := s.database(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "DELETE FROM snapshots WHERE agent_id=?", id)
	return err
}

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

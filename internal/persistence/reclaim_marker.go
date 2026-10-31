package persistence

import (
	"context"
	"database/sql"
	"errors"

	"github.com/oines/astral/internal/domain"
)

const reclaimMarkerKind = "storage_reclaim_dirty"
const reclaimMarkerID = reclaimMarkerKind + ":owner"

// MarkStorageReclaim participates in the transaction removing Host data or its
// final deletion intent. Recovery can therefore discover the need to reclaim
// even if the process exits before a best-effort in-memory notification runs.
func (tx *Tx) MarkStorageReclaim(ctx context.Context) error {
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO operations(id,kind,state,data) VALUES(?,?,?,'{}') ON CONFLICT(id) DO UPDATE SET state=excluded.state WHERE operations.kind=excluded.kind`, reclaimMarkerID, reclaimMarkerKind, domain.ID("reclaim"))
	return err
}

func (s *Store) StorageReclaimDirty(ctx context.Context) (string, error) {
	var token string
	err := s.db.QueryRowContext(ctx, "SELECT state FROM operations WHERE id=? AND kind=?", reclaimMarkerID, reclaimMarkerKind).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return token, err
}

// ClearStorageReclaimDirty clears only the deletion generation included in one
// completed maintenance pass. A concurrent deletion's newer token survives.
func (s *Store) ClearStorageReclaimDirty(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM operations WHERE id=? AND kind=? AND state=?", reclaimMarkerID, reclaimMarkerKind, token)
	return err
}

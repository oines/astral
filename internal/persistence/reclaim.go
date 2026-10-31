package persistence

import (
	"context"
	"fmt"
)

// Reclaim runs outside a transaction on one connection. A busy checkpoint is
// an error, not evidence that deleted pages were returned to the filesystem.
// Callers schedule it during Host idle time and retain failures for retry.
func (s *Store) Reclaim(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	checkpoint := func() error {
		var busy, log, done int
		if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &done); err != nil {
			return err
		}
		if busy != 0 {
			return fmt.Errorf("host database checkpoint busy (%d/%d frames)", done, log)
		}
		return nil
	}
	if err = checkpoint(); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "VACUUM"); err != nil {
		return err
	}
	return checkpoint()
}

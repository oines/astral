package recall

import (
	"context"
	"fmt"
	"time"
)

// Reclaim excludes index/snapshot maintenance and does not create an unused
// recall database merely to reclaim it.
func (s *Service) Reclaim(ctx context.Context) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !s.mu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.db == nil {
		return nil
	}
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
			return fmt.Errorf("recall database checkpoint busy (%d/%d frames)", done, log)
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

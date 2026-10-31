package recall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReclaimDoesNotCreateUnusedIndexAndPreservesExistingIndex(t *testing.T) {
	f := newFixture(t)
	if err := f.recall.Reclaim(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.store.Directory, "recall.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reclaim created an unused index")
	}
	f.recall.mu.Lock()
	db, err := f.recall.database(f.ctx)
	f.recall.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(f.ctx, "INSERT INTO snapshots(id,agent_id,kind,request,expires_at) VALUES('keep','bot','search','{}',9999999999999)"); err != nil {
		t.Fatal(err)
	}
	if err = f.recall.Reclaim(f.ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM snapshots WHERE id='keep'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("recall snapshot lost: %d %v", count, err)
	}
	f.recall.mu.Lock()
	defer f.recall.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err = f.recall.Reclaim(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked reclaim ignored cancellation: %v", err)
	}
}

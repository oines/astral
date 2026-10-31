package storage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/persistence"
)

func markReclaim(t *testing.T, store *persistence.Store) string {
	t.Helper()
	if err := store.Update(context.Background(), func(tx *persistence.Tx) error { return tx.MarkStorageReclaim(context.Background()) }); err != nil {
		t.Fatal(err)
	}
	token, err := store.StorageReclaimDirty(context.Background())
	if err != nil || token == "" {
		t.Fatalf("missing marker: %q %v", token, err)
	}
	return token
}
func reclaimComplete(s *Service) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reclaim.State == "complete"
}

func TestStartupDiscoversDeletionCommittedBeforeQueueOrCallback(t *testing.T) {
	var calls atomic.Int64
	history := &historyStore{reclaim: func(context.Context) error { calls.Add(1); return nil }}
	s, store := fixture(t, history)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	token := markReclaim(t, store)
	restarted := New(context.Background(), s.config)
	defer restarted.Close(context.Background())
	restarted.mu.Lock()
	initial, observed := restarted.reclaim.State, restarted.reclaimObserved
	restarted.mu.Unlock()
	if initial != "queued" || observed != token {
		t.Fatalf("startup lost pending deletion: %q %q", initial, observed)
	}
	await(t, func() bool { return reclaimComplete(restarted) })
	if calls.Load() != 1 {
		t.Fatalf("unexpected reclaim count: %d", calls.Load())
	}
	if dirty, err := store.StorageReclaimDirty(context.Background()); err != nil || dirty != "" {
		t.Fatalf("completed generation remains dirty: %q %v", dirty, err)
	}
}

func TestFailedDirtyGenerationSurvivesRestartWithoutAutomaticRetry(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	history := &historyStore{reclaim: func(context.Context) error {
		calls.Add(1)
		if fail.Load() {
			return errors.New("failed vacuum")
		}
		return nil
	}}
	s, store := fixture(t, history)
	token := markReclaim(t, store)
	// No callback: the periodic observer must discover this committed deletion.
	await(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.reclaim.State == "failed" })
	if calls.Load() != 1 {
		t.Fatalf("unexpected first reclaim count: %d", calls.Load())
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := New(context.Background(), s.config)
	defer restarted.Close(context.Background())
	restarted.mu.Lock()
	state, observed := restarted.reclaim.State, restarted.reclaimObserved
	restarted.mu.Unlock()
	if state != "failed" || observed != token {
		t.Fatalf("failure was not retained: %q %q", state, observed)
	}
	// Passing a worker poll must not endlessly retry the same failed token.
	time.Sleep(2200 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("failed generation was automatically retried: %d", calls.Load())
	}
	fail.Store(false)
	newToken := markReclaim(t, store)
	if newToken == token {
		t.Fatal("token did not change")
	}
	await(t, func() bool { return reclaimComplete(restarted) })
	if calls.Load() != 2 {
		t.Fatalf("new deletion was not reclaimed exactly once: %d", calls.Load())
	}
}

func TestCompletedPassCannotClearConcurrentDeletionAndNewTokenRestartsCoreStep(t *testing.T) {
	var store *persistence.Store
	var calls atomic.Int64
	secondObserved := make(chan string, 1)
	history := &historyStore{reclaim: func(ctx context.Context) error {
		switch calls.Add(1) {
		case 1:
			return store.Update(ctx, func(tx *persistence.Tx) error { return tx.MarkStorageReclaim(ctx) })
		case 2:
			token, err := store.StorageReclaimDirty(ctx)
			if err != nil {
				return err
			}
			secondObserved <- token
		}
		return nil
	}}
	s, backing := fixture(t, history)
	store = backing
	first := markReclaim(t, store)
	await(t, func() bool {
		if !reclaimComplete(s) {
			return false
		}
		dirty, err := store.StorageReclaimDirty(context.Background())
		return err == nil && dirty == ""
	})
	if calls.Load() != 2 {
		t.Fatalf("concurrent generation was lost or completed Core step reused: %d", calls.Load())
	}
	select {
	case token := <-secondObserved:
		if token == "" || token == first {
			t.Fatalf("old pass cleared a newer marker: %q", token)
		}
	default:
		t.Fatal("new generation never reached Core reclaim")
	}
}

func TestRestartContinuesCompletedDatabaseStepsForSameDirtyToken(t *testing.T) {
	var coreCalls, hostCalls atomic.Int64
	s, store := fixture(t, &historyStore{reclaim: func(context.Context) error { coreCalls.Add(1); return nil }})
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	token := markReclaim(t, store)
	record := reclaimRecord{Reclaim: Reclaim{State: "running"}, Next: 1, ObservedDirtyToken: token}
	if err := store.SaveMaintenance(context.Background(), "storage_reclaim", "owner", "running", record); err != nil {
		t.Fatal(err)
	}
	config := s.config
	config.Reclaimers = []func(context.Context) error{func(context.Context) error { hostCalls.Add(1); return nil }}
	restarted := New(context.Background(), config)
	defer restarted.Close(context.Background())
	await(t, func() bool { return reclaimComplete(restarted) })
	if coreCalls.Load() != 0 || hostCalls.Load() != 1 {
		t.Fatalf("lost durable database progress: core=%d host=%d", coreCalls.Load(), hostCalls.Load())
	}
}

func TestClosePersistsObservedFailureWithShutdownContext(t *testing.T) {
	s, store := fixture(t, &historyStore{})
	token := markReclaim(t, store)
	retained := reclaimRecord{Reclaim: Reclaim{State: "running"}, ObservedDirtyToken: token}
	if err := store.SaveMaintenance(context.Background(), "storage_reclaim", "owner", "running", retained); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.reclaim = Reclaim{State: "failed", Error: "vacuum failed"}
	s.reclaimObserved = token
	s.reclaimDirty = true
	s.mu.Unlock()
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Maintenance(context.Background(), "storage_reclaim", "owner", &retained); err != nil {
		t.Fatal(err)
	}
	if retained.State != "failed" || retained.Error != "vacuum failed" || retained.ObservedDirtyToken != token {
		t.Fatalf("shutdown lost the failure published in memory: %+v", retained)
	}
	restarted := New(context.Background(), s.config)
	defer restarted.Close(context.Background())
	restarted.mu.Lock()
	state := restarted.reclaim.State
	restarted.mu.Unlock()
	if state != "failed" {
		t.Fatalf("normal restart automatically retried an observed failure: %s", state)
	}
}

package recall

import (
	"context"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func markAgentDeleting(t *testing.T, f *fixture, id string) {
	t.Helper()
	var agent domain.Agent
	if err := f.store.Get(f.ctx, "agents", id, &agent); err != nil {
		t.Fatal(err)
	}
	agent.Deletion = true
	if err := f.store.Save(f.ctx, "agents", &agent.Meta, &agent); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteAgentErasesEmptySnapshotsAndPreservesOtherBots(t *testing.T) {
	f := newFixture(t)
	f.session(t, "private", "agent")
	f.append(t, "private", message(core.RoleUser, "private needle"))
	f.session(t, "retained", "other")
	f.append(t, "retained", message(core.RoleUser, "retained needle one"), message(core.RoleAssistant, "retained needle two"))
	if _, err := f.recall.Search(f.ctx, "agent", SearchRequest{Query: "private"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.recall.Search(f.ctx, "agent", SearchRequest{Query: "absent", Source: "im"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.recall.List(f.ctx, "agent", ListRequest{Title: "absent"}); err != nil {
		t.Fatal(err)
	}
	retained, err := f.recall.Search(f.ctx, "other", SearchRequest{Query: "needle", Limit: 1})
	if err != nil || len(retained.Hits) != 1 || retained.NextCursor == "" {
		t.Fatalf("retained Bot search: %+v, %v", retained, err)
	}
	markAgentDeleting(t, f, "agent")
	if err = f.core.DeleteSession(f.ctx, "private"); err != nil {
		t.Fatal(err)
	}
	if err = f.recall.DeleteSession(f.ctx, "private", "private"); err != nil {
		t.Fatal(err)
	}
	var empty int
	if err = f.recall.db.QueryRowContext(f.ctx, "SELECT count(*) FROM snapshots WHERE agent_id='agent'").Scan(&empty); err != nil || empty != 2 {
		t.Fatalf("empty snapshots must survive per-Session cleanup: %d, %v", empty, err)
	}
	for range 2 {
		if err = f.recall.DeleteAgent(f.ctx, "agent"); err != nil {
			t.Fatal(err)
		}
	}
	var remaining int
	if err = f.recall.db.QueryRowContext(f.ctx, "SELECT count(*) FROM snapshots WHERE agent_id='agent'").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("deleted Bot snapshots retained: %d, %v", remaining, err)
	}
	next, err := f.recall.Search(f.ctx, "other", SearchRequest{Cursor: retained.NextCursor, Limit: 1})
	if err != nil || len(next.Hits) != 1 || next.Hits[0].SessionID != "retained" {
		t.Fatalf("another Bot lost its search cursor/history: %+v, %v", next, err)
	}
}

func TestDeletedAgentCannotRebuildRecallAfterRestart(t *testing.T) {
	for _, purged := range []bool{false, true} {
		name := "deleting"
		if purged {
			name = "purged"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.session(t, "private", "agent")
			f.append(t, "private", message(core.RoleUser, "private needle"))
			if _, err := f.recall.Search(f.ctx, "agent", SearchRequest{Query: "absent", Source: "im"}); err != nil {
				t.Fatal(err)
			}
			markAgentDeleting(t, f, "agent")
			if err := f.recall.DeleteAgent(f.ctx, "agent"); err != nil {
				t.Fatal(err)
			}
			if purged {
				if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error {
					var agent domain.Agent
					if err := tx.Get(f.ctx, "agents", "agent", &agent); err != nil {
						return err
					}
					return tx.Purge(f.ctx, "agents", agent.ID, agent.Revision)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.recall.Close(); err != nil {
				t.Fatal(err)
			}
			f.recall = New(f.store, f.core)
			// Retaining the source Session/Core here makes any accidental lazy
			// rebuild observable even if a Host cleanup was interrupted.
			if _, err := f.recall.Search(f.ctx, "agent", SearchRequest{Query: "needle"}); !domain.IsMissing(err) {
				t.Fatalf("deleted Bot could search/rebuild: %v", err)
			}
			if _, err := f.recall.List(f.ctx, "agent", ListRequest{}); !domain.IsMissing(err) {
				t.Fatalf("deleted Bot could create an empty snapshot: %v", err)
			}
			if _, err := f.recall.Read(f.ctx, "agent", "private", ReadRequest{}); !domain.IsMissing(err) {
				t.Fatalf("deleted Bot could read/rebuild history: %v", err)
			}
			if err := f.recall.DeleteAgent(f.ctx, "agent"); err != nil {
				t.Fatal("restart cleanup retry:", err)
			}
			for _, table := range []string{"snapshots", "snapshot_sessions", "blocks", "entries", "catalog_heads"} {
				var count int
				if err := f.recall.db.QueryRowContext(f.ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("deleted Bot rebuilt %s: %d, %v", table, count, err)
				}
			}
		})
	}
}

type delayedRecallState struct {
	core.Store
	loaded  chan struct{}
	release chan struct{}
}

func (s *delayedRecallState) LoadState(ctx context.Context, id string) (core.RuntimeState, error) {
	state, err := s.Store.LoadState(ctx, id)
	close(s.loaded)
	select {
	case <-s.release:
		return state, err
	case <-ctx.Done():
		return core.RuntimeState{}, ctx.Err()
	}
}

func TestReadStartedBeforeAgentDeletionCannotRecreateIndex(t *testing.T) {
	f := newFixture(t)
	f.session(t, "private", "agent")
	f.append(t, "private", message(core.RoleUser, "private needle"))
	delayed := &delayedRecallState{Store: f.core, loaded: make(chan struct{}), release: make(chan struct{})}
	f.recall.core = delayed
	finished := make(chan error, 1)
	go func() {
		_, err := f.recall.Read(f.ctx, "agent", "private", ReadRequest{})
		finished <- err
	}()
	select {
	case <-delayed.loaded:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	markAgentDeleting(t, f, "agent")
	if err := f.recall.DeleteSession(f.ctx, "private", "private"); err != nil {
		t.Fatal(err)
	}
	if err := f.recall.DeleteAgent(f.ctx, "agent"); err != nil {
		t.Fatal(err)
	}
	close(delayed.release)
	if err := <-finished; !domain.IsMissing(err) {
		t.Fatalf("late Read rebuilt a deleted Bot's index: %v", err)
	}
	var count int
	if err := f.recall.db.QueryRowContext(f.ctx, "SELECT count(*) FROM blocks").Scan(&count); err != nil || count != 0 {
		t.Fatalf("late Read retained private indexed text: %d, %v", count, err)
	}
}

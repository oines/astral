package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func outboxWorkFixture(t testing.TB) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for at := range 1436 {
		id, status := fmt.Sprintf("history-%04d", at), "accepted"
		if at == 1435 {
			id, status = "pending", "pending"
		}
		r := domain.Outbox{Meta: domain.Meta{ID: id}, GroupID: "group-" + id, BindingID: "binding", ConversationID: "chat", Status: status, Parts: []domain.OutboxPart{{Type: "text", Text: strings.Repeat("ordinary accepted reply ", 64)}}}
		if err = s.Save(context.Background(), "outbox", &r.Meta, &r); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func readOutboxWork(ctx context.Context, s *Store, legacy bool) ([]domain.Outbox, error) {
	var rows []Record
	var err error
	if legacy {
		rows, err = s.List(ctx, "outbox")
	} else {
		rows, err = s.OutboxWork(ctx)
	}
	if err != nil {
		return nil, err
	}
	all := make([]domain.Outbox, 0, len(rows))
	for _, row := range rows {
		var value domain.Outbox
		if err = json.Unmarshal(row.Data, &value); err != nil {
			return nil, err
		}
		all = append(all, value)
	}
	// Keep the production comparator unchanged on both sides of the audit.
	slices.SortStableFunc(all, func(a, b domain.Outbox) int {
		if a.GroupID == b.GroupID {
			return a.Fragment - b.Fragment
		}
		if v := a.CreatedAt.Compare(b.CreatedAt); v != 0 {
			return v
		}
		return strings.Compare(a.ID, b.ID)
	})
	return all, nil
}

func TestOutboxWorkExcludesAcceptedWithoutSchemaOrReadWrites(t *testing.T) {
	s := outboxWorkFixture(t)
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.OutboxWork(t.Context())
	if err != nil || len(rows) != 1 || rows[0].ID != "pending" {
		t.Fatalf("work set=%d err=%v", len(rows), err)
	}
	wal, err := os.Stat(filepath.Join(s.Directory, "astral.db-wal"))
	if err != nil || wal.Size() != 0 {
		t.Fatalf("read wrote WAL: %v %v", wal, err)
	}
	var version, indexes int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 6 {
		t.Fatalf("format=%d err=%v", version, err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name LIKE 'outbox_dispatch_%'").Scan(&indexes); err != nil || indexes != 0 {
		t.Fatalf("added indexes=%d err=%v", indexes, err)
	}
}

func TestOutboxWorkRetainsAllDependencyStatusesAndTransitions(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, status := range []string{"pending", "dispatching", "failed", "blocked", "unknown", "future-status", "accepted"} {
		r := domain.Outbox{Meta: domain.Meta{ID: status}, Status: status}
		if err = s.Save(t.Context(), "outbox", &r.Meta, &r); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.OutboxWork(t.Context())
	if err != nil || len(rows) != 6 {
		t.Fatalf("dependency states=%d err=%v", len(rows), err)
	}
	var pending domain.Outbox
	if err = s.Get(t.Context(), "outbox", "pending", &pending); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"dispatching", "accepted"} {
		pending.Status = status
		if err = s.Save(t.Context(), "outbox", &pending.Meta, &pending); err != nil {
			t.Fatal(err)
		}
		rows, err = s.OutboxWork(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		found := slices.ContainsFunc(rows, func(row Record) bool { return row.ID == "pending" })
		if found != (status != "accepted") {
			t.Fatalf("transition %s returned wrong work set", status)
		}
	}
}

func TestOutboxFilteredReadDoesNotAddTransitionWrites(t *testing.T) {
	source := outboxWorkFixture(t)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := os.ReadFile(filepath.Join(source.Directory, "astral.db"))
	if err != nil {
		t.Fatal(err)
	}
	var baseline int64
	for _, legacy := range []bool{true, false} {
		dir := t.TempDir()
		if err = os.WriteFile(filepath.Join(dir, "astral.db"), db, 0600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		var record domain.Outbox
		if err = s.Get(t.Context(), "outbox", "pending", &record); err != nil {
			t.Fatal(err)
		}
		for _, status := range []string{"pending", "dispatching", "accepted"} {
			if status != "pending" {
				record.Status = status
				if err = s.Save(t.Context(), "outbox", &record.Meta, &record); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = readOutboxWork(t.Context(), s, legacy); err != nil {
				t.Fatal(err)
			}
		}
		wal, err := os.Stat(filepath.Join(dir, "astral.db-wal"))
		if err != nil {
			t.Fatal(err)
		}
		if legacy {
			baseline = wal.Size()
		} else if wal.Size() != baseline {
			t.Fatalf("filtered reads changed transition WAL: before=%d after=%d", baseline, wal.Size())
		}
		t.Logf("OUTBOX_TRANSITION_WAL legacy=%t bytes=%d", legacy, wal.Size())
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

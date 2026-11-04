package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
)

func dispatchFixture(t testing.TB) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func dispatchRecord(t testing.TB, s *Store, id, group, bucket, status string, fragment int) {
	t.Helper()
	r := domain.Outbox{Meta: domain.Meta{ID: id, CreatedAt: time.Date(2026, 11, 4, 0, 0, 0, fragment, time.UTC)}, GroupID: group, BindingID: "binding", ConversationID: bucket, Status: status, Fragment: fragment, Parts: []domain.OutboxPart{{Type: "text", Text: strings.Repeat("ordinary accepted reply ", 64)}}}
	if err := s.Save(context.Background(), "outbox", &r.Meta, &r); err != nil {
		t.Fatal(err)
	}
}
func TestOutboxDispatchReadsOnlyIndexedWorkingSet(t *testing.T) {
	s := dispatchFixture(t)
	ctx := context.Background()
	for i := range 1435 {
		dispatchRecord(t, s, fmt.Sprintf("accepted-%04d", i), fmt.Sprintf("old-%04d", i), "chat", "accepted", 0)
	}
	dispatchRecord(t, s, "pending", "current", "chat", "pending", 0)
	rows, err := s.OutboxDispatchCandidates(ctx, 4)
	if err != nil || len(rows) != 1 || rows[0].ID != "pending" {
		t.Fatalf("working set=%v err=%v", len(rows), err)
	}
	if len(rows[0].Data) > 4096 {
		t.Fatal("query returned historical payloads")
	}
	plan, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+outboxDispatchQuery, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	indexed := false
	for plan.Next() {
		var a, b, c int
		var detail string
		if err = plan.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "SEARCH p USING INDEX outbox_dispatch_status") {
			indexed = true
		}
	}
	if !indexed {
		t.Fatal("pending selection did not use its status index")
	}
}
func TestOutboxDispatchKeepsFragmentAndBucketOrdering(t *testing.T) {
	s := dispatchFixture(t)
	dispatchRecord(t, s, "failed", "group-failed", "a", "failed", 0)
	dispatchRecord(t, s, "dependent", "group-failed", "a", "pending", 1)
	dispatchRecord(t, s, "unrelated", "new-group", "a", "pending", 0)
	dispatchRecord(t, s, "active", "active-group", "b", "dispatching", 0)
	dispatchRecord(t, s, "blocked-bucket", "later-group", "b", "pending", 1)
	dispatchRecord(t, s, "head", "ordered-group", "c", "pending", 0)
	dispatchRecord(t, s, "tail", "ordered-group", "c", "pending", 1)
	rows, err := s.OutboxDispatchCandidates(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, row := range rows {
		got[row.ID] = true
	}
	if len(got) != 2 || !got["unrelated"] || !got["head"] {
		t.Fatalf("wrong eligible heads: %v", got)
	}
}
func TestOutboxIndexesMigrateFormatSixWithoutRewritingReceipts(t *testing.T) {
	dir, db := currentMigrationFixture(t)
	body := `{"id":"preserved","revision":8,"status":"accepted","group_id":"group","created_at":"2026-11-04T00:00:00Z","parts":[{"type":"text","text":"already sent"}],"request_digest":"frozen-digest"}`
	if _, err := db.Exec("INSERT INTO documents(kind,id,revision,data) VALUES('outbox','preserved',8,?)", []byte(body)); err != nil {
		t.Fatal(err)
	}
	execMigrationSQL(t, db, "INSERT INTO outbox_keys(intent_key,outbox_id) VALUES('stable-intent','preserved');INSERT INTO deliveries(event_id,session_id,state,error) VALUES('received-event','session','routed','');")
	execMigrationSQL(t, db, "DROP INDEX outbox_dispatch_status;DROP INDEX outbox_dispatch_group;DROP INDEX outbox_dispatch_bucket;DELETE FROM goose_db_version WHERE version_id>6;PRAGMA user_version=6;")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err = s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name LIKE 'outbox_dispatch_%'").Scan(&n); err != nil || n != 3 {
		t.Fatalf("dispatch indexes=%d err=%v", n, err)
	}
	var retained []byte
	var revision int64
	if err = s.db.QueryRow("SELECT revision,data FROM documents WHERE kind='outbox' AND id='preserved'").Scan(&revision, &retained); err != nil || revision != 8 || string(retained) != body {
		t.Fatalf("index migration rewrote the frozen outbox: revision=%d err=%v", revision, err)
	}
	var outboxID, state string
	if err = s.db.QueryRow("SELECT outbox_id FROM outbox_keys WHERE intent_key='stable-intent'").Scan(&outboxID); err != nil || outboxID != "preserved" {
		t.Fatalf("idempotency identity changed: %q %v", outboxID, err)
	}
	if err = s.db.QueryRow("SELECT state FROM deliveries WHERE event_id='received-event'").Scan(&state); err != nil || state != "routed" {
		t.Fatalf("input receipt changed: %q %v", state, err)
	}
}

func BenchmarkOutbox1435AcceptedOnePending(b *testing.B) {
	s := dispatchFixture(b)
	for i := range 1435 {
		dispatchRecord(b, s, fmt.Sprintf("accepted-%04d", i), fmt.Sprintf("old-%04d", i), "chat", "accepted", 0)
	}
	dispatchRecord(b, s, "pending", "current", "chat", "pending", 0)
	b.Run("previous-full-history-decode", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			rows, err := s.List(context.Background(), "outbox")
			if err != nil {
				b.Fatal(err)
			}
			for _, row := range rows {
				var value domain.Outbox
				if err = json.Unmarshal(row.Data, &value); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("indexed-working-set", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			rows, err := s.OutboxDispatchCandidates(context.Background(), 4)
			if err != nil || len(rows) != 1 {
				b.Fatalf("rows=%d err=%v", len(rows), err)
			}
			var value domain.Outbox
			if err = json.Unmarshal(rows[0].Data, &value); err != nil {
				b.Fatal(err)
			}
		}
	})
}

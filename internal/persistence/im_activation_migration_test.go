package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
)

func TestMigrationInitializesActivationAtPreviouslyDeliveredInboxBoundary(t *testing.T) {
	ctx := context.Background()
	directory, db := baselineMigrationFixture(t)
	// These are frozen format-3 documents, not values marshalled through current
	// channel/domain structs. The lower probe cursor must not reset activation,
	// and the binding with no inbox cursor must remain unseeded.
	execMigrationSQL(t, db, `
INSERT INTO documents(kind,id,revision,data) VALUES
('inbox_cursors','bound-chat',2,'{"id":"bound-chat","revision":2,"created_at":"2026-10-27T12:00:00Z","updated_at":"2026-10-28T12:00:00Z","sequence":72}'),
('inbox_cursors','zero-chat',1,'{"id":"zero-chat","revision":1,"created_at":"2026-10-27T12:00:00Z","updated_at":"2026-10-27T12:00:00Z","sequence":0}'),
('inbox_cursors','large-chat',1,'{"id":"large-chat","revision":1,"created_at":"2026-10-27T12:00:00Z","updated_at":"2026-10-27T12:00:00Z","sequence":9007199254740993}'),
('bindings','bound-chat',1,'{"id":"bound-chat","revision":1,"session_id":"session-old","start_sequence":4}'),
('bindings','new-chat',1,'{"id":"new-chat","revision":1,"session_id":"session-new","start_sequence":73}'),
('probe_cursors','bound-chat',1,'{"id":"bound-chat","revision":1,"sequence":5}');
INSERT INTO versions(kind,id,revision,data) SELECT kind,id,revision,data FROM documents;
INSERT INTO versions(kind,id,revision,data) VALUES
('inbox_cursors','bound-chat',1,'{"id":"bound-chat","revision":1,"created_at":"2026-10-27T12:00:00Z","updated_at":"2026-10-27T12:00:00Z","sequence":5}');
INSERT INTO observations(sequence,id,connection_id,conversation_id,external_id,payload) VALUES
(72,'old-direct','account','conversation','telegram-72','{"id":"old-direct","trigger":"mention","text":"old message"}'),
(73,'not-delivered','account','conversation','telegram-73','{"id":"not-delivered","trigger":"private","text":"new message"}');
INSERT INTO deliveries(event_id,session_id,state,error) VALUES('old-direct:trigger','session-old','applied','');
INSERT INTO operations(id,kind,state,data) VALUES('old-runtime','retained-runtime','done','{"input_id":"old-direct:trigger","kind":"steer","preserve":true}');
`)
	before := activationMigrationRetained(t, db)
	status, err := InspectMigration(ctx, directory)
	if err != nil || status.Current != 3 || status.Target != FormatVersion || !status.NeedsMigration {
		t.Fatalf("format-3 inspection: %+v %v", status, err)
	}
	if err = Migrate(ctx, directory); err != nil {
		t.Fatal(err)
	}
	status, err = InspectMigration(ctx, directory)
	if err != nil || status.Current != FormatVersion || status.NeedsMigration {
		t.Fatalf("activation migration status: %+v %v", status, err)
	}
	if after := activationMigrationRetained(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration rewrote retained inbox/history/receipts:\nbefore: %v\nafter: %v", before, after)
	}

	rows, err := db.Query("SELECT id,revision,data FROM documents WHERE kind='im_activation_cursors' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"bound-chat": 72, "zero-chat": 0, "large-chat": 9007199254740993}
	seen := map[string]int64{}
	for rows.Next() {
		var id string
		var revision int64
		var data []byte
		var cursor struct {
			ID        string `json:"id"`
			Revision  int64  `json:"revision"`
			Sequence  int64  `json:"sequence"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
		}
		if err = rows.Scan(&id, &revision, &data); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &cursor); err != nil {
			t.Fatal(err)
		}
		if cursor.ID != id || revision != 1 || cursor.Revision != revision || cursor.CreatedAt == "" || cursor.UpdatedAt == "" {
			t.Fatalf("invalid activation metadata: %s %s", id, data)
		}
		seen[id] = cursor.Sequence
	}
	if err = closeRows(rows, rows.Err()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("activation boundaries: got %v, want %v", seen, want)
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM versions WHERE kind='im_activation_cursors'").Scan(&count); err != nil || count != len(want) {
		t.Fatalf("activation initial revisions: %d %v", count, err)
	}

	// A later migration run must neither rewind live activation nor append a
	// second initial revision. The original inbox history remains byte-for-byte.
	execMigrationSQL(t, db, `UPDATE documents SET data=json_set(data,'$.sequence',73) WHERE kind='im_activation_cursors' AND id='bound-chat';`)
	if err = Migrate(ctx, directory); err != nil {
		t.Fatal(err)
	}
	var sequence int64
	if err = db.QueryRow("SELECT json_extract(data,'$.sequence') FROM documents WHERE kind='im_activation_cursors' AND id='bound-chat'").Scan(&sequence); err != nil || sequence != 73 {
		t.Fatalf("repeated migration reset activation: %d %v", sequence, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM versions WHERE kind='im_activation_cursors'").Scan(&count); err != nil || count != len(want) {
		t.Fatalf("repeated migration duplicated revisions: %d %v", count, err)
	}
	if after := activationMigrationRetained(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("repeated migration changed retained records")
	}
}

func TestMigrationActivationSeedsRollbackWithTheFormatAndLedger(t *testing.T) {
	ctx := context.Background()
	directory, db := baselineMigrationFixture(t)
	execMigrationSQL(t, db, `
INSERT INTO documents(kind,id,revision,data) VALUES('inbox_cursors','bound-chat',1,'{"id":"bound-chat","revision":1,"sequence":72}');
INSERT INTO versions(kind,id,revision,data) VALUES('im_activation_cursors','bound-chat',1,'{"id":"bound-chat","revision":1,"sequence":1}');
`)
	if err := Migrate(ctx, directory); err == nil {
		t.Fatal("conflicting activation history was silently overwritten")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM documents WHERE kind='im_activation_cursors'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed migration committed activation documents: %d %v", count, err)
	}
	status, err := InspectMigration(ctx, directory)
	if err != nil || status.Current != 3 || !status.NeedsMigration {
		t.Fatalf("failed activation seed advanced format or ledger: %+v %v", status, err)
	}
	execMigrationSQL(t, db, "DELETE FROM versions WHERE kind='im_activation_cursors';")
	if err = Migrate(ctx, directory); err != nil {
		t.Fatalf("activation migration did not resume after conflict repair: %v", err)
	}
}

func TestFreshDatabaseDoesNotSeedActivationCursors(t *testing.T) {
	_, db := migrationFixture(t)
	var count int
	if err := db.QueryRow("SELECT count(*) FROM documents WHERE kind='im_activation_cursors'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("new database seeded activation documents: %d %v", count, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM versions WHERE kind='im_activation_cursors'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("new database seeded activation revisions: %d %v", count, err)
	}
}

func activationMigrationRetained(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	queries := map[string]string{
		"documents":    "SELECT json_group_array(json_array(kind,id,revision,hex(data))) FROM (SELECT * FROM documents WHERE kind<>'im_activation_cursors' ORDER BY kind,id)",
		"versions":     "SELECT json_group_array(json_array(kind,id,revision,hex(data))) FROM (SELECT * FROM versions WHERE kind<>'im_activation_cursors' ORDER BY kind,id,revision)",
		"observations": "SELECT json_group_array(json_array(sequence,id,connection_id,conversation_id,external_id,hex(payload))) FROM (SELECT * FROM observations ORDER BY sequence)",
		"deliveries":   "SELECT json_group_array(json_array(event_id,session_id,state,error)) FROM (SELECT * FROM deliveries ORDER BY event_id)",
		"operations":   "SELECT json_group_array(json_array(id,kind,state,hex(data))) FROM (SELECT * FROM operations ORDER BY id)",
	}
	result := make(map[string]string, len(queries))
	for name, query := range queries {
		var data string
		if err := db.QueryRow(query).Scan(&data); err != nil {
			t.Fatal(err)
		}
		result[name] = data
	}
	return result
}

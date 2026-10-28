package platform_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/platform"
)

// Frozen public baseline schemas. They deliberately do not use current store
// initialization or current format constants when constructing migration data.
const startupLegacyHostSchema = `
CREATE TABLE documents(kind TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(kind,id));
CREATE TABLE versions(kind TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(kind,id,revision));
CREATE TABLE secrets(id TEXT PRIMARY KEY,value BLOB NOT NULL);
CREATE TABLE observations(sequence INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,connection_id TEXT NOT NULL,conversation_id TEXT NOT NULL,external_id TEXT NOT NULL,payload BLOB NOT NULL,UNIQUE(connection_id,external_id));
CREATE INDEX observation_conversation ON observations(conversation_id,sequence);
CREATE TABLE deliveries(event_id TEXT PRIMARY KEY,session_id TEXT NOT NULL,state TEXT NOT NULL,error TEXT NOT NULL DEFAULT '');
CREATE TABLE operations(id TEXT PRIMARY KEY,kind TEXT NOT NULL,state TEXT NOT NULL,data BLOB NOT NULL);
CREATE TABLE outbox_keys(intent_key TEXT PRIMARY KEY,outbox_id TEXT NOT NULL);
INSERT INTO documents(kind,id,revision,data) VALUES('inbox_cursors','retained-chat',1,'{"id":"retained-chat","revision":1,"sequence":7}');
INSERT INTO versions(kind,id,revision,data) SELECT kind,id,revision,data FROM documents;
PRAGMA user_version=3;
`

const startupLegacyCoreSchema = `
CREATE TABLE format(version INTEGER NOT NULL);
INSERT INTO format VALUES(11);
CREATE TABLE rollouts(id TEXT PRIMARY KEY,status TEXT NOT NULL,seq INTEGER NOT NULL);
CREATE TABLE sessions(id TEXT PRIMARY KEY,rollout_id TEXT NOT NULL,ready INTEGER NOT NULL,state BLOB NOT NULL,fork_digest TEXT NOT NULL DEFAULT '');
CREATE INDEX session_rollout ON sessions(rollout_id,id);
CREATE TABLE entries(rollout_id TEXT NOT NULL,session_id TEXT NOT NULL,id TEXT NOT NULL,seq INTEGER NOT NULL,version INTEGER NOT NULL,parent_id TEXT NOT NULL,kind TEXT NOT NULL,reference TEXT NOT NULL,payload BLOB NOT NULL,projection BLOB,PRIMARY KEY(rollout_id,id),UNIQUE(rollout_id,seq));
CREATE INDEX entry_parent ON entries(rollout_id,parent_id,seq) WHERE projection IS NOT NULL;
CREATE INDEX entry_history ON entries(rollout_id,seq) WHERE projection IS NOT NULL;
CREATE INDEX entry_kind ON entries(rollout_id,kind,seq);
CREATE INDEX entry_reference ON entries(rollout_id,kind,reference,seq);
CREATE INDEX entry_writer ON entries(rollout_id,session_id,seq);
CREATE TABLE receipts(rollout_id TEXT NOT NULL,session_id TEXT NOT NULL,commit_id TEXT NOT NULL,version INTEGER NOT NULL,digest TEXT NOT NULL,first_seq INTEGER NOT NULL,last_seq INTEGER NOT NULL,PRIMARY KEY(session_id,commit_id));
CREATE INDEX receipt_rollout ON receipts(rollout_id);
CREATE TABLE pending(rollout_id TEXT NOT NULL,session_id TEXT NOT NULL,kind TEXT NOT NULL,input_kind TEXT NOT NULL,key TEXT NOT NULL,owner TEXT NOT NULL,seq INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(session_id,kind,key));
CREATE INDEX pending_order ON pending(session_id,kind,input_kind,seq);
CREATE INDEX pending_owner ON pending(session_id,kind,owner);
CREATE INDEX pending_rollout ON pending(rollout_id);
CREATE TABLE inputs(session_id TEXT NOT NULL,id TEXT NOT NULL,kind TEXT NOT NULL,digest TEXT NOT NULL,accepted_at TEXT NOT NULL,payload BLOB NOT NULL,state TEXT NOT NULL,entry_id TEXT NOT NULL DEFAULT '',PRIMARY KEY(session_id,id));
CREATE INDEX input_status ON inputs(session_id,kind,state);
CREATE TABLE attempts(session_id TEXT NOT NULL,id TEXT NOT NULL,rollout_id TEXT NOT NULL,version INTEGER NOT NULL,purpose TEXT NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(session_id,id),UNIQUE(session_id,version));
CREATE INDEX attempt_purpose ON attempts(session_id,purpose,version);
CREATE INDEX attempt_rollout ON attempts(rollout_id);
PRAGMA user_version=11;
`

func TestStartupFailureRestoresBothStoresBeforeReleasingLock(t *testing.T) {
	data := filepath.Join(t.TempDir(), "server")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	for _, source := range []struct{ name, schema string }{{"astral.db", startupLegacyHostSchema}, {"core.db", startupLegacyCoreSchema}} {
		db, err := sql.Open("sqlite", filepath.Join(data, source.name))
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec("PRAGMA journal_mode=WAL;" + source.schema)
		closeErr := db.Close()
		if err != nil || closeErr != nil {
			t.Fatal(errors.Join(err, closeErr))
		}
	}
	if err := os.WriteFile(filepath.Join(data, "master.key"), bytes.Repeat([]byte{0x42}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range []string{"astral.db", "core.db", "master.key"} {
		var err error
		before[name], err = os.ReadFile(filepath.Join(data, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := platform.New(platform.Config{DataDirectory: data, ComputerDirectory: filepath.Join(t.TempDir(), "computers"), Listen: "invalid:port"}); err == nil {
		t.Fatal("invalid network config started")
	} else if !strings.Contains(err.Error(), "Listen address") {
		t.Fatalf("startup failed before the intended post-migration network validation: %v", err)
	}
	// Startup failure must finish restoring data and release its lock before
	// another owner can inspect or start this installation.
	lock, err := platform.Lock(filepath.Join(data, "server.lock"))
	if err != nil {
		t.Fatalf("startup failure retained the Server lock: %v", err)
	}
	defer lock.Close()
	h, err := persistence.InspectMigration(context.Background(), data)
	if err != nil || h.Current != 3 || !h.NeedsMigration {
		t.Fatalf("Host adoption committed before startup: %+v %v", h, err)
	}
	c, err := sqlite.InspectMigration(context.Background(), filepath.Join(data, "core.db"))
	if err != nil || c.Current != 11 || !c.NeedsMigration {
		t.Fatalf("Core adoption committed before startup: %+v %v", c, err)
	}
	for name, original := range before {
		restored, err := os.ReadFile(filepath.Join(data, name))
		if err != nil || !bytes.Equal(restored, original) {
			t.Fatalf("startup rollback changed original %s bytes: %v", name, err)
		}
	}
	if _, err = os.Stat(filepath.Join(data, "migration-state.json")); !os.IsNotExist(err) {
		t.Fatalf("recovery journal survived successful restoration: %v", err)
	}
}

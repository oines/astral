package datamigrate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/persistence"
)

// These are the actual public baseline schemas. A migration fixture must never
// create the current schema and remove its ledger: once a new step exists that
// would label already-converted data as an old database, or corrupt a new one.
const legacyHostSchema = `
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

const legacyCoreSchema = `
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

func legacyFixture(t *testing.T) string {
	t.Helper()
	data := filepath.Join(t.TempDir(), "server")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	for _, source := range []struct{ name, schema string }{{"astral.db", legacyHostSchema}, {"core.db", legacyCoreSchema}} {
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
	fixtureIdentity(t, data)
	plan, err := Inspect(context.Background(), data)
	if err != nil || plan.Host.Current != 3 || plan.Core.Current != 11 || !plan.Host.NeedsMigration || !plan.Core.NeedsMigration {
		t.Fatalf("legacy fixture is not the frozen baseline: %+v %v", plan, err)
	}
	return data
}

func currentFixture(t *testing.T) string {
	t.Helper()
	data := filepath.Join(t.TempDir(), "server")
	host, err := persistence.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Close(); err != nil {
		t.Fatal(err)
	}
	core, err := sqlite.Open(filepath.Join(data, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = core.Close(); err != nil {
		t.Fatal(err)
	}
	fixtureIdentity(t, data)
	return data
}

func fixtureIdentity(t *testing.T, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(data, "computer-identity"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "computer-identity", "identity.pem"), []byte("test-pairing-identity"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentFormatsWithCompleteLedgersDoNotStartUpgrade(t *testing.T) {
	ctx := context.Background()
	data := currentFixture(t)
	plan, err := Inspect(ctx, data)
	if err != nil || plan.Pending || plan.Host.Current != plan.Host.Target || plan.Core.Current != plan.Core.Target {
		t.Fatalf("current fixture needs migration: %+v %v", plan, err)
	}
	before := map[string][]byte{}
	for _, file := range []string{"astral.db", "core.db", "master.key"} {
		before[file], err = os.ReadFile(filepath.Join(data, file))
		if err != nil {
			t.Fatal(err)
		}
	}
	upgrade, err := Prepare(ctx, data, "0.1.1", nil)
	if err != nil || upgrade != nil {
		t.Fatalf("current complete ledgers were re-adopted: %+v %v", upgrade, err)
	}
	for file, original := range before {
		after, err := os.ReadFile(filepath.Join(data, file))
		if err != nil || !bytes.Equal(after, original) {
			t.Fatalf("no-op modified %s: %v", file, err)
		}
	}
	for _, file := range []string{journalName, resultName} {
		if _, err := os.Stat(filepath.Join(data, file)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("no-op created %s: %v", file, err)
		}
	}
}

func TestNoOpUpgradeDoesNotReplaceLastBackup(t *testing.T) {
	ctx := context.Background()
	data := legacyFixture(t)
	u, err := Prepare(ctx, data, "0.1.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = u.Commit(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(data, resultName))
	if err != nil {
		t.Fatal(err)
	}
	next, err := Prepare(ctx, data, "0.1.1", nil)
	if err != nil || next != nil {
		t.Fatalf("same-format update created a new snapshot: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(data, resultName))
	if err != nil || string(before) != string(after) {
		t.Fatalf("no-op changed old backup record: %v", err)
	}
}

func TestRestoreRejectsPortablePathTraversalBeforeDeletingLiveFiles(t *testing.T) {
	for _, path := range []string{"../outside", "..\\outside", "dir\\..\\server.lock", "C:outside", "key:stream"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			data := legacyFixture(t)
			u, err := Prepare(ctx, data, "0.1.0", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = u.Commit(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(u.Backup, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var m manifest
			if err = json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			m.Files[0].Path = path
			if err = writeJSON(filepath.Join(u.Backup, "manifest.json"), m, protectPrivate); err != nil {
				t.Fatal(err)
			}
			if err = Restore(ctx, data, u.Backup, nil); err == nil {
				t.Fatal("accepted traversing path")
			}
			p, err := Inspect(ctx, data)
			if err != nil || p.Pending {
				t.Fatalf("live data touched: %+v %v", p, err)
			}
		})
	}
}

func TestUpgradeAdoptsBothBaselinesAndCanRestoreWholeSnapshot(t *testing.T) {
	ctx := context.Background()
	data := legacyFixture(t)
	databases := map[string][]byte{}
	for _, name := range []string{"astral.db", "core.db"} {
		var err error
		databases[name], err = os.ReadFile(filepath.Join(data, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	key, err := os.ReadFile(filepath.Join(data, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := Prepare(ctx, data, "0.1.0", nil)
	if err != nil || u == nil {
		t.Fatalf("prepare: %v %+v", err, u)
	}
	p, err := Inspect(ctx, data)
	if err != nil || p.Pending {
		t.Fatalf("target: %+v %v", p, err)
	}
	for name, original := range databases {
		migrated, err := os.ReadFile(filepath.Join(data, name))
		if err != nil || bytes.Equal(migrated, original) {
			t.Fatalf("upgrade did not change the legacy %s: %v", name, err)
		}
	}
	if err = os.WriteFile(filepath.Join(data, "master.key"), []byte("changed-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(data, "new-file"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = u.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(data, "master.key"))
	if err != nil || string(restored) != string(key) {
		t.Fatalf("key did not restore: %v", err)
	}
	if _, err = os.Stat(filepath.Join(data, "new-file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new file survived restore: %v", err)
	}
	p, err = Inspect(ctx, data)
	if err != nil || !p.Pending || p.Host.Current != 3 || p.Core.Current != 11 {
		t.Fatalf("old ledger not restored: %+v %v", p, err)
	}
	for name, original := range databases {
		restored, err := os.ReadFile(filepath.Join(data, name))
		if err != nil || !bytes.Equal(restored, original) {
			t.Fatalf("whole-snapshot restore changed original %s bytes: %v", name, err)
		}
	}
	if _, err = os.Stat(filepath.Join(data, "computer-identity", "identity.pem")); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationFailureRestoresHostCoreAndFilesTogether(t *testing.T) {
	ctx := context.Background()
	data := legacyFixture(t)
	before := map[string][]byte{}
	for _, name := range []string{"astral.db", "core.db", "master.key"} {
		var err error
		before[name], err = os.ReadFile(filepath.Join(data, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	want := errors.New("injected Core migration failure")
	_, err := prepare(ctx, data, "0.1.0", nil, persistence.Migrate, func(_ context.Context, data string) error {
		if err := os.WriteFile(filepath.Join(data, "computer-identity", "identity.pem"), []byte("changed"), 0600); err != nil {
			return err
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("want failure: %v", err)
	}
	p, err := Inspect(ctx, data)
	if err != nil || !p.Host.NeedsMigration || !p.Core.NeedsMigration || p.Host.Current != 3 || p.Core.Current != 11 {
		t.Fatalf("partial upgrade survived: %+v %v", p, err)
	}
	for name, original := range before {
		restored, err := os.ReadFile(filepath.Join(data, name))
		if err != nil || !bytes.Equal(restored, original) {
			t.Fatalf("failed migration did not restore original %s bytes: %v", name, err)
		}
	}
	identity, err := os.ReadFile(filepath.Join(data, "computer-identity", "identity.pem"))
	if err != nil || string(identity) != "test-pairing-identity" {
		t.Fatalf("identity was not restored: %q %v", identity, err)
	}
}

func TestInterruptedUpgradeRestoresBeforeRetryAndCommitStopsRecovery(t *testing.T) {
	ctx := context.Background()
	data := legacyFixture(t)
	u, err := Prepare(ctx, data, "0.1.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(data, "partial-file"), []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := Prepare(ctx, data, "0.1.0", nil)
	if err != nil || next == nil {
		t.Fatalf("resume: %v", err)
	}
	if next.Backup == u.Backup {
		t.Fatal("retry reused an incomplete attempt")
	}
	if _, err = os.Stat(filepath.Join(data, "partial-file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted change survived: %v", err)
	}
	if err = next.Commit(); err != nil {
		t.Fatal(err)
	}
	unchanged, err := Prepare(ctx, data, "0.1.0", nil)
	if err != nil || unchanged != nil {
		t.Fatalf("committed migration repeated: %v", err)
	}
}

func TestCorruptBackupDoesNotTouchLiveData(t *testing.T) {
	ctx := context.Background()
	data := legacyFixture(t)
	u, err := Prepare(ctx, data, "0.1.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = u.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(u.Backup, "files", "master.key"), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, data, u.Backup, nil); err == nil {
		t.Fatal("accepted damaged snapshot")
	}
	p, err := Inspect(ctx, data)
	if err != nil || p.Pending {
		t.Fatalf("live data mutated: %+v %v", p, err)
	}
}

func TestPrepareRejectsUnknownCoreWithoutChangingHost(t *testing.T) {
	ctx := context.Background()
	data := legacyFixture(t)
	db, err := sql.Open("sqlite", filepath.Join(data, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE format SET version=999; PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Prepare(ctx, data, "0.1.0", nil); err == nil {
		t.Fatal("unknown Core format accepted")
	}
	host, err := persistence.InspectMigration(ctx, data)
	if err != nil || !host.NeedsMigration {
		t.Fatalf("Host changed before preflight completed: %+v %v", host, err)
	}
}

func TestMissingCoreWithCurrentOrHistoricalSessionsIsRejectedWithoutWriting(t *testing.T) {
	for _, table := range []string{"documents", "versions"} {
		t.Run(table, func(t *testing.T) {
			data := legacyFixture(t)
			if err := os.Remove(filepath.Join(data, "core.db")); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", filepath.Join(data, "astral.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec("PRAGMA wal_autocheckpoint=0; INSERT INTO " + table + "(kind,id,revision,data) VALUES('sessions','existing-session',1,'{}')"); err != nil {
				t.Fatal(err)
			}
			// Keep the reference in an uncheckpointed WAL: the check must read
			// committed WAL contents without a runtime Open or ledger adoption.
			before, err := os.ReadFile(filepath.Join(data, "astral.db-wal"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Prepare(context.Background(), data, "0.1.0", nil); err == nil || !strings.Contains(err.Error(), "session references") {
				t.Fatalf("missing Core with %s references was accepted: %v", table, err)
			}
			after, err := os.ReadFile(filepath.Join(data, "astral.db-wal"))
			if err != nil || string(before) != string(after) {
				t.Fatalf("rejected preflight changed Host WAL: %v", err)
			}
			if _, err = os.Stat(filepath.Join(data, "core.db")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("preflight initialized an empty Core: %v", err)
			}
			if _, err = os.Stat(filepath.Join(data, journalName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected preflight started an upgrade: %v", err)
			}
		})
	}
}

func TestMissingDatabasesWithRecoverableSidecarsAreRejected(t *testing.T) {
	for _, name := range []string{"astral.db-wal", "astral.db-journal", "core.db-wal", "core.db-journal"} {
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			const residue = "committed or rollback pages retained for recovery"
			if err := os.WriteFile(filepath.Join(data, name), []byte(residue), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Prepare(context.Background(), data, "0.1.0", nil); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("orphan sidecar was treated as fresh data: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(data, name))
			if err != nil || string(raw) != residue {
				t.Fatalf("preflight changed recoverable sidecar: %q %v", raw, err)
			}
			entries, err := os.ReadDir(data)
			if err != nil || len(entries) != 1 || entries[0].Name() != name {
				t.Fatalf("preflight initialized or changed data: %v %v", entries, err)
			}
		})
	}
}

func TestMissingCoreIsAllowedDuringFirstHostInitialization(t *testing.T) {
	data := t.TempDir()
	host, err := persistence.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Close(); err != nil {
		t.Fatal(err)
	}
	plan, err := Inspect(context.Background(), data)
	if err != nil || plan.Host.Missing || !plan.Core.Missing || plan.Pending {
		t.Fatalf("fresh Host cannot initialize its first Core: %+v %v", plan, err)
	}
	upgrade, err := Prepare(context.Background(), data, "0.1.0", nil)
	if err != nil || upgrade != nil {
		t.Fatalf("fresh Host was treated as damaged history: %v", err)
	}
	if _, err = os.Stat(filepath.Join(data, "core.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created Core instead of leaving initialization to Open: %v", err)
	}
}

func TestCurrentDataStillRequiresItsOriginalMasterKey(t *testing.T) {
	for _, missing := range []bool{true, false} {
		name := "invalid"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			data := legacyFixture(t)
			upgrade, err := Prepare(ctx, data, "0.1.0", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = upgrade.Commit(); err != nil {
				t.Fatal(err)
			}
			plan, err := Inspect(ctx, data)
			if err != nil || plan.Pending {
				t.Fatalf("fixture was not fully adopted: %+v %v", plan, err)
			}
			before := make(map[string][]byte)
			for _, file := range []string{"astral.db", "core.db", resultName} {
				before[file], err = os.ReadFile(filepath.Join(data, file))
				if err != nil {
					t.Fatal(err)
				}
			}
			key := filepath.Join(data, "master.key")
			const invalidKey = "invalid-key"
			if missing {
				err = os.Remove(key)
			} else {
				err = os.WriteFile(key, []byte(invalidKey), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if next, err := Prepare(ctx, data, "0.1.1", nil); err == nil || next != nil || !strings.Contains(err.Error(), "master.key") {
				t.Fatalf("current data bypassed key validation: %v", err)
			}
			if raw, err := os.ReadFile(key); missing {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing key was replaced: %v", err)
				}
			} else if err != nil || string(raw) != invalidKey {
				t.Fatalf("invalid key was replaced: %q %v", raw, err)
			}
			for file, original := range before {
				raw, err := os.ReadFile(filepath.Join(data, file))
				if err != nil || string(raw) != string(original) {
					t.Fatalf("key rejection modified %s: %v", file, err)
				}
			}
			if _, err = os.Stat(filepath.Join(data, journalName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("key rejection started a new upgrade: %v", err)
			}
		})
	}
}

func TestPrepareEmptyDirectoryLeavesFreshInitializationToStores(t *testing.T) {
	data := t.TempDir()
	upgrade, err := Prepare(context.Background(), data, "0.1.0", nil)
	if err != nil || upgrade != nil {
		t.Fatalf("empty data requires an existing master key: %v", err)
	}
	entries, err := os.ReadDir(data)
	if err != nil || len(entries) != 0 {
		t.Fatalf("maintenance initialized fresh data: %v %v", entries, err)
	}
}

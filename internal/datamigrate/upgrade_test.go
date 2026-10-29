package datamigrate

import (
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

func fixture(t *testing.T) string {
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
	for _, name := range []string{"astral.db", "core.db"} {
		db, err := sql.Open("sqlite", filepath.Join(data, name))
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec("DROP TABLE IF EXISTS goose_db_version")
		if err == nil && name == "astral.db" {
			_, err = db.Exec("PRAGMA user_version=3")
		}
		closeErr := db.Close()
		if err != nil || closeErr != nil {
			t.Fatal(errors.Join(err, closeErr))
		}
	}
	if err = os.MkdirAll(filepath.Join(data, "computer-identity"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(data, "computer-identity", "identity.pem"), []byte("test-pairing-identity"), 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestNoOpUpgradeDoesNotReplaceLastBackup(t *testing.T) {
	ctx := context.Background()
	data := fixture(t)
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
			data := fixture(t)
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
	data := fixture(t)
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
	if err != nil || !p.Pending {
		t.Fatalf("old ledger not restored: %+v %v", p, err)
	}
	if _, err = os.Stat(filepath.Join(data, "computer-identity", "identity.pem")); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationFailureRestoresHostCoreAndFilesTogether(t *testing.T) {
	ctx := context.Background()
	data := fixture(t)
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
	if err != nil || !p.Host.NeedsMigration || !p.Core.NeedsMigration {
		t.Fatalf("partial upgrade survived: %+v %v", p, err)
	}
	identity, err := os.ReadFile(filepath.Join(data, "computer-identity", "identity.pem"))
	if err != nil || string(identity) != "test-pairing-identity" {
		t.Fatalf("identity was not restored: %q %v", identity, err)
	}
}

func TestInterruptedUpgradeRestoresBeforeRetryAndCommitStopsRecovery(t *testing.T) {
	ctx := context.Background()
	data := fixture(t)
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
	data := fixture(t)
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
	data := fixture(t)
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
			data := fixture(t)
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
			data := fixture(t)
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

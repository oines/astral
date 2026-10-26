package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

func migrationFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "astral.db")
	st, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return directory, db
}

func execMigrationSQL(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationNewDatabaseAndReadOnlyInspection(t *testing.T) {
	ctx := context.Background()
	directory, db := migrationFixture(t)
	path := filepath.Join(directory, "astral.db")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	filesBefore, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	status, err := InspectMigration(ctx, directory)
	if err != nil || status != (MigrationStatus{Current: 3, Target: 3}) {
		t.Fatalf("new database: %+v %v", status, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("inspection changed the database: %v", err)
	}
	filesAfter, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(filesBefore) != len(filesAfter) {
		t.Fatalf("inspection created files: %v -> %v", filesBefore, filesAfter)
	}
	missing := filepath.Join(t.TempDir(), "uncreated")
	status, err = InspectMigration(ctx, missing)
	if err != nil || status != (MigrationStatus{Target: 3, Missing: true}) {
		t.Fatalf("missing database: %+v %v", status, err)
	}
	if err = Migrate(ctx, missing); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection or migration created a missing parent: %v", err)
	}
}

func TestMigrationAdoptsOnlySupportedBaselineAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	directory, db := migrationFixture(t)
	path := filepath.Join(directory, "astral.db")
	current, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	var credentialID string
	if err = current.Update(ctx, func(tx *Tx) error {
		var putErr error
		credentialID, putErr = tx.PutSecret(ctx, "migration-test-credential")
		return putErr
	}); err != nil {
		t.Fatal(err)
	}
	if err = current.Close(); err != nil {
		t.Fatal(err)
	}
	keyBefore, err := os.ReadFile(filepath.Join(directory, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	execMigrationSQL(t, db, "DROP TABLE goose_db_version")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	status, err := InspectMigration(ctx, directory)
	if err != nil || status != (MigrationStatus{Current: 3, Target: 3, NeedsMigration: true}) {
		t.Fatalf("baseline inspection: %+v %v", status, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("inspection initialized the ledger: %v", err)
	}
	for range 2 {
		if err = Migrate(ctx, directory); err != nil {
			t.Fatal(err)
		}
	}
	status, err = InspectMigration(ctx, directory)
	if err != nil || status.NeedsMigration || status.Current != 3 {
		t.Fatalf("adopted baseline: %+v %v", status, err)
	}
	st, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	credential, err := st.Secret(ctx, credentialID)
	if err != nil || credential != "migration-test-credential" {
		t.Fatalf("baseline changed encrypted credentials: %q %v", credential, err)
	}
	keyAfter, err := os.ReadFile(filepath.Join(directory, "master.key"))
	if err != nil || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatalf("baseline replaced master.key: %v", err)
	}
	var count int
	if err = st.db.QueryRow("SELECT count(*) FROM goose_db_version").Scan(&count); err != nil || count != 2 {
		t.Fatalf("baseline applied more than once: %d %v", count, err)
	}
}

func TestMigrationResumesAfterLedgerInitializationWithoutRelabellingData(t *testing.T) {
	directory, db := migrationFixture(t)
	execMigrationSQL(t, db, "DELETE FROM goose_db_version WHERE version_id=3")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := InspectMigration(context.Background(), directory)
	if err != nil || status.Current != 3 || !status.NeedsMigration {
		t.Fatalf("interrupted ledger initialization: %+v %v", status, err)
	}
	if err = Migrate(context.Background(), directory); err != nil {
		t.Fatal(err)
	}
	status, err = InspectMigration(context.Background(), directory)
	if err != nil || status.Current != 3 || status.NeedsMigration {
		t.Fatalf("baseline resume: %+v %v", status, err)
	}
}

func TestMigrationFreshTargetSchemaRecordsHistoryWithoutRunningOldTransforms(t *testing.T) {
	_, db := migrationFixture(t)
	plan := testMigrationPlan(false)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DROP TABLE goose_db_version; PRAGMA user_version=5;"); err != nil {
		t.Fatal(err)
	}
	if err = initializeMigrationHistory(context.Background(), db, tx, plan); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	status, err := inspectMigration(context.Background(), db, plan)
	if err != nil || status.Current != 5 || status.NeedsMigration {
		t.Fatalf("fresh schema history: %+v %v", status, err)
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='migration_order'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("fresh schema ran a historical transformation: %d %v", count, err)
	}
}

func testMigrationPlan(failLast bool) migrationPlan {
	return migrationPlan{
		files: fstest.MapFS{
			"00003_baseline.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n")},
			"00004_order.sql":    {Data: []byte("-- +goose Up\nCREATE TABLE migration_order(step INTEGER);\nINSERT INTO migration_order VALUES(4);\n")},
		},
		goMigrations: []*goose.Migration{goose.NewGoMigration(5, &goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			var steps int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM migration_order WHERE step=4").Scan(&steps); err != nil {
				return err
			}
			if steps != 1 {
				return errors.New("Go migration did not see the preceding SQL migration")
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO migration_order VALUES(5)"); err != nil {
				return err
			}
			if err := convertFrozenV4JSON(ctx, tx); err != nil {
				return err
			}
			if failLast {
				return errors.New("injected conversion failure")
			}
			return nil
		}}, nil)},
		target: 5,
	}
}

func migrationSteps(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.Query("SELECT step FROM migration_order ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var steps []int
	for rows.Next() {
		var step int
		if err = rows.Scan(&step); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, step)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return steps
}

func TestMigrationRunsAllMissingSQLAndGoStepsInOrder(t *testing.T) {
	ctx := context.Background()
	directory, db := migrationFixture(t)
	plan := testMigrationPlan(false)
	status, err := inspectMigration(ctx, db, plan)
	if err != nil || status.Current != 3 || status.Target != 5 || !status.NeedsMigration {
		t.Fatalf("before: %+v %v", status, err)
	}
	for range 2 {
		if err = migrateDatabase(ctx, db, plan); err != nil {
			t.Fatal(err)
		}
	}
	if steps := migrationSteps(t, db); !reflect.DeepEqual(steps, []int{4, 5}) {
		t.Fatalf("migration order or duplicate application: %v", steps)
	}
	status, err = inspectMigration(ctx, db, plan)
	if err != nil || status.Current != 5 || status.NeedsMigration {
		t.Fatalf("after: %+v %v", status, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if st, err := Open(directory); err == nil {
		if st != nil {
			st.Close()
		}
		t.Fatalf("normal Open accepted a future format: %v", err)
	}
}

// This test migration deliberately owns its input shape. Unknown fields remain
// RawMessage values, so historical large integers survive without float casts.
func convertFrozenV4JSON(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"documents", "versions"} {
		rows, err := tx.QueryContext(ctx, "SELECT id,revision,data FROM "+table+" WHERE kind='migration_fixture'")
		if err != nil {
			return err
		}
		type frozenRow struct {
			id       string
			revision int64
			data     []byte
		}
		var records []frozenRow
		for rows.Next() {
			var record frozenRow
			if err = rows.Scan(&record.id, &record.revision, &record.data); err != nil {
				rows.Close()
				return err
			}
			records = append(records, record)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		for _, record := range records {
			var old struct {
				LegacyName string `json:"legacy_name"`
			}
			if err = json.Unmarshal(record.data, &old); err != nil {
				return err
			}
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(record.data, &fields); err != nil {
				return err
			}
			fields["display_name"], err = json.Marshal(old.LegacyName)
			if err != nil {
				return err
			}
			delete(fields, "legacy_name")
			data, err := json.Marshal(fields)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET data=? WHERE kind='migration_fixture' AND id=? AND revision=?", data, record.id, record.revision); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestMigrationFrozenJSONConvertsCurrentAndHistoricalRevisions(t *testing.T) {
	ctx := context.Background()
	_, db := migrationFixture(t)
	const current = `{"legacy_name":"current","counter":9007199254740993123,"extra":{"keep":true}}`
	const historical = `{"legacy_name":"previous","counter":9007199254740993124,"extra":{"keep":true}}`
	if _, err := db.Exec("INSERT INTO documents(kind,id,revision,data) VALUES('migration_fixture','sample',2,?);", current); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO versions(kind,id,revision,data) VALUES('migration_fixture','sample',1,?);", historical); err != nil {
		t.Fatal(err)
	}
	if err := migrateDatabase(ctx, db, testMigrationPlan(true)); err == nil {
		t.Fatal("expected injected JSON conversion failure")
	}
	for table, original := range map[string]string{"documents": current, "versions": historical} {
		var data string
		if err := db.QueryRow("SELECT data FROM " + table + " WHERE kind='migration_fixture'").Scan(&data); err != nil || data != original {
			t.Fatalf("failed step changed %s: %s %v", table, data, err)
		}
	}
	if err := migrateDatabase(ctx, db, testMigrationPlan(false)); err != nil {
		t.Fatal(err)
	}
	for table, expected := range map[string]string{
		"documents": `{"display_name":"current","counter":9007199254740993123,"extra":{"keep":true}}`,
		"versions":  `{"display_name":"previous","counter":9007199254740993124,"extra":{"keep":true}}`,
	} {
		var data []byte
		if err := db.QueryRow("SELECT data FROM " + table + " WHERE kind='migration_fixture'").Scan(&data); err != nil {
			t.Fatal(err)
		}
		var got, want map[string]json.RawMessage
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(expected), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s migration lost historical data: %s", table, data)
		}
	}
}

func TestMigrationFailureRollsBackDataMarkersAndLedgerThenResumes(t *testing.T) {
	ctx := context.Background()
	_, db := migrationFixture(t)
	plan := testMigrationPlan(true)
	if err := migrateDatabase(ctx, db, plan); err == nil || !strings.Contains(err.Error(), "injected conversion failure") {
		t.Fatalf("expected injected failure, got %v", err)
	}
	status, err := inspectMigration(ctx, db, plan)
	if err != nil || status.Current != 4 || !status.NeedsMigration {
		t.Fatalf("failed step advanced markers or ledger: %+v %v", status, err)
	}
	if steps := migrationSteps(t, db); !reflect.DeepEqual(steps, []int{4}) {
		t.Fatalf("failed step did not roll back: %v", steps)
	}
	if err = migrateDatabase(ctx, db, testMigrationPlan(false)); err != nil {
		t.Fatal(err)
	}
	if steps := migrationSteps(t, db); !reflect.DeepEqual(steps, []int{4, 5}) {
		t.Fatalf("resume repeated or skipped a step: %v", steps)
	}
}

func TestMigrationRejectsUnknownAndInconsistentFormatsBeforeWriting(t *testing.T) {
	for _, mutation := range []string{
		"PRAGMA user_version=2;",
		"PRAGMA user_version=4;",
		"PRAGMA user_version=0;",
		"PRAGMA user_version=1;",
	} {
		t.Run(mutation, func(t *testing.T) {
			directory, db := migrationFixture(t)
			path := filepath.Join(directory, "astral.db")
			execMigrationSQL(t, db, mutation)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = InspectMigration(context.Background(), directory); !errors.Is(err, errMigrationFormat) {
				t.Fatalf("inspection accepted unknown format: %v", err)
			}
			if err = Migrate(context.Background(), directory); !errors.Is(err, errMigrationFormat) {
				t.Fatalf("migration accepted unknown format: %v", err)
			}
			if st, err := Open(directory); err == nil {
				if st != nil {
					st.Close()
				}
				t.Fatalf("Open accepted unknown format: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("unsupported database was changed: %v", err)
			}
		})
	}
}

func TestMigrationRejectsLedgerGapsAndUnregisteredSteps(t *testing.T) {
	ctx := context.Background()
	for _, mutation := range []string{
		"INSERT INTO goose_db_version(version_id,is_applied) VALUES(4,1);",
		"DELETE FROM goose_db_version WHERE version_id=0;",
		"UPDATE goose_db_version SET is_applied=0 WHERE version_id=3;",
		"DELETE FROM goose_db_version;",
	} {
		t.Run(mutation, func(t *testing.T) {
			_, db := migrationFixture(t)
			execMigrationSQL(t, db, mutation)
			if _, err := inspectMigration(ctx, db, currentMigrationPlan()); !errors.Is(err, errMigrationCorrupt) {
				t.Fatalf("invalid ledger was accepted: %v", err)
			}
		})
	}
	_, db := migrationFixture(t)
	plan := testMigrationPlan(false)
	delete(plan.files.(fstest.MapFS), "00004_order.sql")
	if err := migrateDatabase(ctx, db, plan); err == nil || !strings.Contains(err.Error(), "not sequential") {
		t.Fatalf("binary with missing migration was accepted: %v", err)
	}
}

func TestMigrationRejectsNonDatabaseAndHonorsCancellation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "astral.db")
	if err := os.WriteFile(path, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectMigration(context.Background(), directory); !errors.Is(err, errMigrationFormat) {
		t.Fatalf("invalid header: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectMigration(ctx, directory); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
}

func TestMigrationInspectionSeesUncheckpointedWAL(t *testing.T) {
	directory, db := migrationFixture(t)
	path := filepath.Join(directory, "astral.db")
	execMigrationSQL(t, db, "PRAGMA wal_autocheckpoint=0; PRAGMA user_version=4;")
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("fixture did not retain a WAL: %v", err)
	}
	before := make(map[string][]byte)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		before[suffix] = data
	}
	status, err := InspectMigration(context.Background(), directory)
	if !errors.Is(err, errMigrationFormat) || status.Current != 4 {
		t.Fatalf("inspection ignored the WAL: %+v %v", status, err)
	}
	for suffix, data := range before {
		after, err := os.ReadFile(path + suffix)
		if err != nil || !bytes.Equal(data, after) {
			t.Fatalf("inspection changed original %q: %v", suffix, err)
		}
	}
}

func TestMigrationRejectsNonTransactionalAndEnvironmentDependentSteps(t *testing.T) {
	ctx := context.Background()
	for _, annotation := range []string{"NO TRANSACTION", "no transaction", "ENVSUB ON"} {
		t.Run(annotation, func(t *testing.T) {
			_, db := migrationFixture(t)
			plan := testMigrationPlan(false)
			plan.files.(fstest.MapFS)["00004_order.sql"].Data = []byte("-- +goose " + annotation + "\n-- +goose Up\nCREATE TABLE migration_order(step INTEGER);\n")
			if err := migrateDatabase(ctx, db, plan); err == nil {
				t.Fatal("unsafe SQL migration accepted")
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='migration_order'").Scan(&count); err != nil || count != 0 {
				t.Fatalf("unsafe migration ran before rejection: %d %v", count, err)
			}
		})
	}
	_, db := migrationFixture(t)
	plan := testMigrationPlan(false)
	plan.goMigrations = []*goose.Migration{goose.NewGoMigration(5, &goose.GoFunc{RunDB: func(context.Context, *sql.DB) error {
		t.Fatal("nontransactional Go migration executed")
		return nil
	}}, nil)}
	if err := migrateDatabase(ctx, db, plan); err == nil {
		t.Fatal("nontransactional Go migration accepted")
	}
}

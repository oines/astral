package persistence

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	hostmigrations "github.com/oines/astral/internal/persistence/migrations"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

const BaselineVersion int64 = 3

var (
	errMigrationFormat  = errors.New("unsupported Astral database format")
	errMigrationCorrupt = errors.New("inconsistent Astral migration history")
)

//go:embed migrations/*.sql
var migrations embed.FS

// MigrationStatus describes the durable Host format, rather than an application
// release number. NeedsMigration also covers adopting the format-3 baseline
// into Goose's ledger without changing the data format.
type MigrationStatus struct {
	Current        int64 `json:"current"`
	Target         int64 `json:"target"`
	NeedsMigration bool  `json:"needs_migration"`
	Missing        bool  `json:"missing"`
}

type migrationPlan struct {
	files        fs.FS
	goMigrations []*goose.Migration
	target       int64
}

func currentMigrationPlan() migrationPlan {
	files, _ := fs.Sub(migrations, "migrations")
	return migrationPlan{files: files, goMigrations: hostGoMigrations(), target: FormatVersion}
}

// InspectMigration reads an existing database without initializing a schema,
// creating a file or preparing its parent directories. A missing file is not an
// error: Open initializes new databases directly at the current format.
//
// The caller must stop all database users before inspecting for an upgrade and
// keep them stopped through backup, Migrate and any restoration. This API does
// not supply the Host's process ownership lock or a multi-database transaction.
func InspectMigration(ctx context.Context, directory string) (MigrationStatus, error) {
	if err := ctx.Err(); err != nil {
		return MigrationStatus{}, err
	}
	plan := currentMigrationPlan()
	abs, missing, err := migrationPath(directory)
	if err != nil {
		return MigrationStatus{}, err
	}
	if missing {
		return MigrationStatus{Target: plan.target, Missing: true}, nil
	}
	db, cleanup, err := openInspectionDatabase(ctx, abs)
	if err != nil {
		return MigrationStatus{}, err
	}
	defer cleanup()
	return inspectMigration(ctx, db, plan)
}

// HasSessionReferences checks whether a missing Core database would strand
// existing Host sessions. It is read-only and must use the same offline
// ownership as InspectMigration; it never initializes a schema or master key.
func HasSessionReferences(ctx context.Context, directory string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	abs, missing, err := migrationPath(directory)
	if err != nil || missing {
		return false, err
	}
	db, cleanup, err := openInspectionDatabase(ctx, abs)
	if err != nil {
		return false, err
	}
	defer cleanup()
	var present bool
	err = db.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM documents WHERE kind='sessions') OR
		EXISTS(SELECT 1 FROM versions WHERE kind='sessions')`).Scan(&present)
	return present, err
}

func openInspectionDatabase(ctx context.Context, path string) (*sql.DB, func(), error) {
	source, immutable, cleanup, err := inspectionSource(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	uri := migrationURI(source, runtime.GOOS)
	query := "?mode=ro"
	if immutable {
		query += "&immutable=1"
	}
	db, err := sql.Open("sqlite", uri.String()+query)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	return db, func() { _ = db.Close(); cleanup() }, nil
}

// SQLite mode=ro can create WAL auxiliaries; immutable=1 ignores existing WAL
// content. Use the fast immutable path only for checkpointed databases. A crash
// WAL is inspected on a temporary copy, so the original database and every
// sidecar remain untouched. The Host's offline ownership makes the copy stable.
func inspectionSource(ctx context.Context, path string) (string, bool, func(), error) {
	info, err := os.Stat(path + "-wal")
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.Size() == 0) {
		return path, true, func() {}, nil
	}
	if err != nil {
		return "", false, nil, err
	}
	dir, err := os.MkdirTemp("", "astral-host-inspect-")
	if err != nil {
		return "", false, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	copyPath := filepath.Join(dir, "astral.db")
	for _, suffix := range []string{"", "-wal"} {
		if err = copyInspectionFile(ctx, path+suffix, copyPath+suffix); err != nil {
			cleanup()
			return "", false, nil, err
		}
	}
	return copyPath, false, cleanup, nil
}

func copyInspectionFile(ctx context.Context, source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, inspectionReader{ctx: ctx, Reader: in})
	closeErr := out.Close()
	return errors.Join(err, closeErr)
}

type inspectionReader struct {
	ctx context.Context
	io.Reader
}

func (r inspectionReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

// Migrate applies every missing Host migration in ascending order. Migrations
// are embedded in the latest binary; intermediate releases are not required.
// Each step and its format marker are committed with its Goose ledger entry
// in one SQLite transaction. On failure, that step is rolled back, and no later
// step runs. Earlier successful steps remain committed for retry or restoration.
//
// Migrate is an offline operation. The Host owns shutdown, exclusive process
// ownership, backup and coordinated recovery of Host, Core and external files.
// Unknown formats (including pre-baseline formats) are never relabelled. Missing
// databases are left missing, and no downgrade operation is provided.
func Migrate(ctx context.Context, directory string) error {
	status, err := InspectMigration(ctx, directory)
	if err != nil || status.Missing || !status.NeedsMigration {
		return err
	}
	abs, missing, err := migrationPath(directory)
	if err != nil || missing {
		if missing {
			return fmt.Errorf("host migration database disappeared: %w", os.ErrNotExist)
		}
		return err
	}
	uri := migrationURI(abs, runtime.GOOS)
	db, err := sql.Open("sqlite", uri.String()+"?mode=rw&_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return migrateDatabase(ctx, db, currentMigrationPlan())
}

// Resolving an inspection path must not create directories or databases.
func migrationPath(directory string) (string, bool, error) {
	if directory == "" {
		return "", false, errors.New("host data directory is required")
	}
	abs, err := filepath.Abs(filepath.Join(directory, "astral.db"))
	if err != nil {
		return "", false, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if errors.Is(err, os.ErrNotExist) {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	var head [16]byte
	if _, err = io.ReadFull(f, head[:]); err != nil || string(head[:]) != "SQLite format 3\x00" {
		return "", false, errMigrationFormat
	}
	return abs, false, nil
}

func newMigrationProvider(db *sql.DB, plan migrationPlan) (*goose.Provider, error) {
	for _, migration := range plan.goMigrations {
		if migration == nil || !migration.UseTx || migration.UpFnNoTxContext != nil {
			return nil, errors.New("host Go migrations require a transaction-scoped up function")
		}
	}
	store, err := database.NewStore(goose.DialectSQLite3, goose.DefaultTablename)
	if err != nil {
		return nil, err
	}
	p, err := goose.NewProvider(goose.DialectCustom, db, plan.files,
		goose.WithStore(migrationStore{Store: store}),
		goose.WithDisableGlobalRegistry(true),
		goose.WithGoMigrations(plan.goMigrations...),
	)
	if err != nil {
		return nil, err
	}
	sources := p.ListSources()
	// Formats are a sequential, immutable chain, independent of release tags.
	// Reject missing steps in the binary before opening a migration transaction.
	for i, source := range sources {
		if source.Version != BaselineVersion+int64(i) {
			return nil, fmt.Errorf("host migration chain is not sequential at %d", source.Version)
		}
		if source.Type == goose.TypeSQL {
			contents, err := fs.ReadFile(plan.files, source.Path)
			if err != nil {
				return nil, err
			}
			for _, line := range strings.Split(string(contents), "\n") {
				if !strings.Contains(line, "+goose") {
					continue
				}
				annotation := strings.TrimSpace(strings.Replace(strings.ReplaceAll(line, "--", ""), "+goose", "", 1))
				if strings.EqualFold(annotation, "NO TRANSACTION") {
					return nil, fmt.Errorf("host SQL migration %d disables its transaction", source.Version)
				}
				if strings.EqualFold(annotation, "ENVSUB ON") {
					return nil, fmt.Errorf("host SQL migration %d depends on environment substitution", source.Version)
				}
			}
		}
	}
	if len(sources) == 0 || sources[len(sources)-1].Version != plan.target {
		return nil, fmt.Errorf("host migration target does not match format %d", plan.target)
	}
	return p, nil
}

func readFormatMarkers(ctx context.Context, db database.DBTxConn) (int64, error) {
	var version int64
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func inspectMigration(ctx context.Context, db *sql.DB, plan migrationPlan) (MigrationStatus, error) {
	status := MigrationStatus{Target: plan.target}
	if _, err := newMigrationProvider(db, plan); err != nil {
		return status, err
	}
	format, err := readFormatMarkers(ctx, db)
	if err != nil {
		return status, err
	}
	for _, table := range []string{"documents", "versions", "secrets", "observations", "deliveries", "operations", "outbox_keys"} {
		var count int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", table).Scan(&count); err != nil {
			return status, err
		}
		if count != 1 {
			return status, fmt.Errorf("host schema is missing %s: %w", table, errMigrationFormat)
		}
	}
	status.Current = format
	if format < BaselineVersion || format > plan.target {
		return status, fmt.Errorf("host format %d cannot migrate to %d: %w", format, plan.target, errMigrationFormat)
	}
	var hasLedger bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name=?)", goose.DefaultTablename).Scan(&hasLedger); err != nil {
		return status, err
	}
	if !hasLedger {
		if format != BaselineVersion {
			return status, fmt.Errorf("host format %d has no migration ledger: %w", format, errMigrationCorrupt)
		}
		status.NeedsMigration = true
		return status, nil
	}
	rows, err := db.QueryContext(ctx, "SELECT version_id, is_applied FROM goose_db_version ORDER BY id")
	if err != nil {
		return status, fmt.Errorf("host migration ledger: %w: %v", errMigrationCorrupt, err)
	}
	defer rows.Close()
	previous := int64(-1)
	for rows.Next() {
		var version int64
		var applied bool
		if err := rows.Scan(&version, &applied); err != nil {
			return status, err
		}
		expected := previous + 1
		if previous == 0 {
			expected = BaselineVersion
		}
		if !applied || version != expected || version > format {
			return status, fmt.Errorf("host migration ledger is not an applied prefix at %d: %w", version, errMigrationCorrupt)
		}
		previous = version
	}
	if err := rows.Err(); err != nil {
		return status, err
	}
	if previous != format && (previous != 0 || format != BaselineVersion) {
		return status, fmt.Errorf("host format %d and migration ledger %d disagree: %w", format, previous, errMigrationCorrupt)
	}
	status.NeedsMigration = previous < plan.target
	return status, nil
}

func migrateDatabase(ctx context.Context, db *sql.DB, plan migrationPlan) error {
	status, err := inspectMigration(ctx, db, plan)
	if err != nil || !status.NeedsMigration {
		return err
	}
	p, err := newMigrationProvider(db, plan)
	if err != nil {
		return err
	}
	if _, err = p.Up(ctx); err != nil {
		return fmt.Errorf("host migration: %w", err)
	}
	status, err = inspectMigration(ctx, db, plan)
	if err != nil {
		return err
	}
	if status.NeedsMigration {
		return errors.New("host migration did not reach the target format")
	}
	return nil
}

// A newly created database already has the target schema. Record the complete
// chain without applying historical transformations to current-format data.
func initializeMigrationHistory(ctx context.Context, db *sql.DB, tx *sql.Tx, plan migrationPlan) error {
	p, err := newMigrationProvider(db, plan)
	if err != nil {
		return err
	}
	store, err := database.NewStore(goose.DialectSQLite3, goose.DefaultTablename)
	if err != nil {
		return err
	}
	if err = store.CreateVersionTable(ctx, tx); err != nil {
		return err
	}
	if err = store.Insert(ctx, tx, database.InsertRequest{Version: 0}); err != nil {
		return err
	}
	for _, source := range p.ListSources() {
		if err = store.Insert(ctx, tx, database.InsertRequest{Version: source.Version}); err != nil {
			return err
		}
	}
	return nil
}

// Goose inserts the ledger entry using the same transaction as the migration.
// Keeping the Host format marker here prevents a migration author from
// committing converted data while the database still advertises an old format.
type migrationStore struct{ database.Store }

func (s migrationStore) Insert(ctx context.Context, db database.DBTxConn, req database.InsertRequest) error {
	if req.Version != 0 {
		if _, ok := db.(*sql.Tx); !ok {
			return errors.New("host migration requires a transaction")
		}
		format, err := readFormatMarkers(ctx, db)
		if err != nil {
			return err
		}
		expected := req.Version - 1
		if req.Version == BaselineVersion {
			expected = BaselineVersion
		}
		if format != expected {
			return fmt.Errorf("host migration %d starts from format %d: %w", req.Version, format, errMigrationFormat)
		}
		if _, err = db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d;", req.Version)); err != nil {
			return err
		}
	}
	return s.Store.Insert(ctx, db, req)
}

// Add immutable, self-contained JSON/data transformations here. A migration's
// input structs belong to that migration, never to the current domain package.
func hostGoMigrations() []*goose.Migration {
	return []*goose.Migration{hostmigrations.BoundSessionsVisible(), hostmigrations.SessionPrivacy()}
}

func migrationURI(path, system string) url.URL {
	if system == "windows" {
		path = strings.ReplaceAll(path, `\`, "/")
		if strings.HasPrefix(path, "//?/UNC/") {
			path = "//" + path[8:]
		} else {
			path = strings.TrimPrefix(path, "//?/")
		}
		if len(path) >= 2 && path[1] == ':' {
			path = "/" + path
		}
	}
	return url.URL{Scheme: "file", Path: path}
}

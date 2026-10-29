package platform_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/platform"
)

func TestStartupFailureRestoresBothStoresBeforeReleasingLock(t *testing.T) {
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
		if _, err = db.Exec("DROP TABLE goose_db_version"); err != nil {
			t.Fatal(err)
		}
		if name == "astral.db" {
			if _, err = db.Exec("PRAGMA user_version=3"); err != nil {
				t.Fatal(err)
			}
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = platform.New(platform.Config{DataDirectory: data, ComputerDirectory: filepath.Join(t.TempDir(), "computers"), Listen: "invalid:port"}); err == nil {
		t.Fatal("invalid network config started")
	}
	h, err := persistence.InspectMigration(context.Background(), data)
	if err != nil || !h.NeedsMigration {
		t.Fatalf("Host adoption committed before startup: %+v %v", h, err)
	}
	c, err := sqlite.InspectMigration(context.Background(), filepath.Join(data, "core.db"))
	if err != nil || !c.NeedsMigration {
		t.Fatalf("Core adoption committed before startup: %+v %v", c, err)
	}
	if _, err = os.Stat(filepath.Join(data, "migration-state.json")); !os.IsNotExist(err) {
		t.Fatalf("recovery journal survived successful restoration: %v", err)
	}
}

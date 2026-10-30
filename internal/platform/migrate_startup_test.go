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
	old, err := os.ReadFile("../datamigrate/testdata/core-schema11.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(data, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(old)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, name := range []string{"astral.db", "core.db"} {
		db, err := sql.Open("sqlite", filepath.Join(data, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("DROP TABLE IF EXISTS goose_db_version"); err != nil {
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

func TestMaintenanceMarkerCoversPreparationUntilStartupCommit(t *testing.T) {
	data := filepath.Join(t.TempDir(), "server")
	server, err := platform.New(platform.Config{DataDirectory: data, ComputerDirectory: filepath.Join(t.TempDir(), "computers"), Listen: "127.0.0.1:0", NoLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	marker := filepath.Join(data, "startup-maintenance.json")
	if _, err = os.Stat(marker); err != nil {
		t.Fatalf("maintenance ended before Start commit: %v", err)
	}
	if err = server.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("committed startup retained marker: %v", err)
	}
}

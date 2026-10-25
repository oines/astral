package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/platform"
)

func TestWeixinMaintenanceRequiresExistingOfflineStore(t *testing.T) {
	ctx := context.Background()
	var out, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "not-a-server")
	if err := maintenance(ctx, []string{"weixin-wire-ownership", "--data", missing}, &out, &stderr); err == nil {
		t.Fatal("maintenance created a new store instead of requiring existing data")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing directory was changed: %v", err)
	}
	directory := t.TempDir()
	store, err := persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := platform.Lock(filepath.Join(directory, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = maintenance(ctx, []string{"weixin-wire-ownership", "--data", directory}, &out, &stderr); err == nil || !strings.Contains(err.Error(), "stop Astral") {
		t.Fatalf("maintenance ignored the running Server lock: %v", err)
	}
}

func TestWeixinMaintenanceRequiresExplicitAnonymousCachePurge(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	var credential string
	err = store.Update(ctx, func(tx *persistence.Tx) error {
		var err error
		credential, err = tx.PutSecret(ctx, "fixture-wire-secret")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	record := struct {
		domain.Meta
		CredentialID string `json:"credential_id"`
	}{Meta: domain.Meta{ID: "anonymous-wire"}, CredentialID: credential}
	if err = store.Save(ctx, "weixin_wire", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	args := []string{"weixin-wire-ownership", "--data", directory}
	if err = maintenance(ctx, args, &out, &stderr); err == nil || !strings.Contains(err.Error(), "--purge-unowned") {
		t.Fatalf("anonymous cache was silently removed: %v", err)
	}
	if strings.Contains(out.String()+stderr.String(), "fixture-wire-secret") {
		t.Fatal("maintenance exposed credentials")
	}
	store, err = persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Secret(ctx, credential); err != nil {
		t.Fatal("default maintenance removed the anonymous credential")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = maintenance(ctx, append(args, "--purge-unowned"), &out, &stderr); err != nil {
		t.Fatal(err)
	}
	store, err = persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.Secret(ctx, credential); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("explicit purge left anonymous credential: %v", err)
	}
	if records, err := store.List(ctx, "weixin_wire"); err != nil || len(records) != 0 {
		t.Fatalf("explicit purge left anonymous records: %v %v", records, err)
	}
}

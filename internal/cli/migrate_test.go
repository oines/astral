package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/platform"
)

func TestMigrationRequiresOfflineServerAndDoesNotOpenDataWhenLocked(t *testing.T) {
	data := t.TempDir()
	s, err := persistence.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(data, "astral.db"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := platform.Lock(filepath.Join(data, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	var out, stderr bytes.Buffer
	if err = migrate(context.Background(), []string{"--data", data}, &out, &stderr); err == nil || !strings.Contains(err.Error(), "stop Astral") {
		t.Fatalf("lock bypassed: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(data, "astral.db"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("busy data changed: %v", err)
	}
}

func TestMigrationRejectsAmbiguousRestoreArguments(t *testing.T) {
	for _, args := range [][]string{
		{"restore", "--data", t.TempDir()},
		{"restore", "--data", t.TempDir(), "--latest", "--backup", "somewhere"},
		{"--data", t.TempDir(), "--latest"},
		{"recover", "--data", t.TempDir(), "--status"},
	} {
		var out, stderr bytes.Buffer
		if err := migrate(context.Background(), args, &out, &stderr); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

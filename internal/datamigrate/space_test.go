package datamigrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSpaceCombinesBackupDataAndTemporaryRequirements(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	backup := filepath.Join(root, "backup")
	for _, path := range []string{data, backup} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	needs := []spaceNeed{{data, 60, "conversion"}, {backup, 50, "backup"}}
	same := func(string) (diskSpace, error) { return diskSpace{"same-volume", 100}, nil }
	if err := checkSpace(needs, same); err == nil || !strings.Contains(err.Error(), "110") {
		t.Fatalf("overcommitted shared volume: %v", err)
	}
	separate := func(path string) (diskSpace, error) { return diskSpace{path, 100}, nil }
	if err := checkSpace(needs, separate); err != nil {
		t.Fatal(err)
	}
	if err := checkSpace(needs, func(string) (diskSpace, error) { return diskSpace{}, syscall.EIO }); !errors.Is(err, syscall.EIO) {
		t.Fatal("space probe failure ignored")
	}
}

func TestJournalWriteENOSPCDoesNotStartConversion(t *testing.T) {
	data := fixture(t)
	data, err := filepath.EvalSymlinks(data)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(data, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	protect := func(path string) error {
		if filepath.Dir(path) == data && strings.HasPrefix(filepath.Base(path), ".migration-") {
			return syscall.ENOSPC
		}
		return protectPrivate(path)
	}
	called := false
	step := func(context.Context, string) error { called = true; return nil }
	if _, err = prepare(context.Background(), data, "test", protect, step, step); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("unexpected failure: %v", err)
	}
	if called {
		t.Fatal("conversion ran before durable recovery journal")
	}
	after, err := os.ReadFile(filepath.Join(data, "core.db"))
	if err != nil || string(before) != string(after) {
		t.Fatal("journal failure changed Core data")
	}
}

func TestCancelledConversionLeavesJournalForNextStartupRecovery(t *testing.T) {
	data := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := prepare(ctx, data, "test", nil, func(context.Context, string) error { cancel(); return context.Canceled }, func(context.Context, string) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err = os.Stat(filepath.Join(data, journalName)); err != nil {
		t.Fatalf("lost recovery journal: %v", err)
	}
	upgrade, err := Prepare(context.Background(), data, "retry", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = upgrade.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(data, journalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed retry retained journal")
	}
}

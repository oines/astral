package persistence_test

import (
	"context"
	"errors"
	"testing"

	"github.com/oines/astral/internal/persistence"
)

func TestStorageReclaimMarkerIsTransactionalDurableAndClearsOnlyCapturedToken(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	assertToken := func(want string) {
		t.Helper()
		token, err := store.StorageReclaimDirty(ctx)
		if err != nil || token != want {
			t.Fatalf("reclaim marker: %q, want %q: %v", token, want, err)
		}
	}
	assertToken("")
	rollback := errors.New("rollback deletion")
	err = store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.MarkStorageReclaim(ctx); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	assertToken("")
	if err = store.Update(ctx, func(tx *persistence.Tx) error { return tx.MarkStorageReclaim(ctx) }); err != nil {
		t.Fatal(err)
	}
	first, err := store.StorageReclaimDirty(ctx)
	if err != nil || first == "" {
		t.Fatalf("missing committed marker: %q %v", first, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	assertToken(first)
	if err = store.Update(ctx, func(tx *persistence.Tx) error { return tx.MarkStorageReclaim(ctx) }); err != nil {
		t.Fatal(err)
	}
	second, err := store.StorageReclaimDirty(ctx)
	if err != nil || second == "" || second == first {
		t.Fatalf("new deletion did not replace token: %q %v", second, err)
	}
	if err = store.ClearStorageReclaimDirty(ctx, first); err != nil {
		t.Fatal(err)
	}
	assertToken(second)
	if err = store.ClearStorageReclaimDirty(ctx, ""); err != nil {
		t.Fatal(err)
	}
	assertToken(second)
	if err = store.ClearStorageReclaimDirty(ctx, second); err != nil {
		t.Fatal(err)
	}
	assertToken("")
}

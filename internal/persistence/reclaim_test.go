package persistence_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestReclaimPreservesConfigurationHistoryAndMaintenanceIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	value := domain.Agent{Meta: domain.Meta{ID: "bot"}, Name: "before"}
	if err = store.Save(ctx, "agents", &value.Meta, &value); err != nil {
		t.Fatal(err)
	}
	value.Name = "after"
	if err = store.Save(ctx, "agents", &value.Meta, &value); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err = store.SaveMaintenance(ctx, "storage-test", "op", "running", map[string]any{"count": i, "padding": strings.Repeat("x", 1<<18)}); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Reclaim(ctx); err != nil {
		t.Fatal(err)
	}
	var current, old domain.Agent
	if err = store.Get(ctx, "agents", value.ID, &current); err != nil || current.Name != "after" {
		t.Fatalf("current lost: %+v %v", current, err)
	}
	if err = store.Version(ctx, "agents", value.ID, 1, &old); err != nil || old.Name != "before" {
		t.Fatalf("configuration history lost: %+v %v", old, err)
	}
	var maintenance struct {
		Count int `json:"count"`
	}
	if err = store.Maintenance(ctx, "storage-test", "op", &maintenance); err != nil || maintenance.Count != 3 {
		t.Fatalf("operation lost: %+v %v", maintenance, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = store.Reclaim(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

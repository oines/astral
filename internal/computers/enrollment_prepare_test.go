package computers

import (
	"context"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestContainerPreparationWindowIsAuthenticatedAndBounded(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hub := NewHub(store)
	defer hub.Close()
	ctx := context.Background()
	enrollment, err := hub.Enroll(ctx, "https://astral.example", true, false)
	if err != nil {
		t.Fatal(err)
	}
	id := enrollment.Enrollment.ID
	if err = hub.PrepareContainer(ctx, id, "wrong"); err == nil {
		t.Fatal("unauthenticated preparation")
	}
	if err = hub.PrepareContainer(ctx, id, enrollment.Token); err != nil {
		t.Fatal(err)
	}
	var prepared domain.Enrollment
	if err = store.Get(ctx, "enrollments", id, &prepared); err != nil {
		t.Fatal(err)
	}
	if !prepared.ExpiresAt.Equal(prepared.CreatedAt.Add(90 * time.Minute)) {
		t.Fatal("unexpected preparation deadline")
	}
	if err = hub.PrepareContainer(ctx, id, enrollment.Token); err != nil {
		t.Fatal(err)
	}
	var repeated domain.Enrollment
	if err = store.Get(ctx, "enrollments", id, &repeated); err != nil {
		t.Fatal(err)
	}
	if !repeated.ExpiresAt.Equal(prepared.ExpiresAt) || repeated.Revision != prepared.Revision {
		t.Fatal("retry extended the preparation deadline")
	}
	prepared.ExpiresAt = time.Now().Add(-time.Minute)
	if err = store.Save(ctx, "enrollments", &prepared.Meta, &prepared); err != nil {
		t.Fatal(err)
	}
	if err = hub.PrepareContainer(ctx, id, enrollment.Token); err == nil {
		t.Fatal("revived an expired enrollment")
	}
	native, err := hub.Enroll(ctx, "https://astral.example", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = hub.PrepareContainer(ctx, native.Enrollment.ID, native.Token); err == nil {
		t.Fatal("extended a native enrollment")
	}
}

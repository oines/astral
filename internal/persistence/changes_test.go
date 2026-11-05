package persistence

import (
	"errors"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestChangesPublishesOnlyCommittedWrites(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	changed := s.Changes()
	other := s.Changes()
	assertQuiet := func() {
		t.Helper()
		select {
		case <-changed:
			t.Fatal("uncommitted or unchanged state woke the reader")
		default:
		}
	}
	value := struct {
		domain.Meta
		Name string `json:"name"`
	}{Meta: domain.Meta{ID: "fixture"}, Name: "committed"}
	rolledBack := errors.New("rollback fixture")
	if err := s.Update(t.Context(), func(tx *Tx) error {
		if err := tx.Save(t.Context(), "fixtures", &value.Meta, &value); err != nil {
			return err
		}
		assertQuiet()
		return rolledBack
	}); !errors.Is(err, rolledBack) {
		t.Fatal(err)
	}
	assertQuiet()
	if err := s.Update(t.Context(), func(tx *Tx) error {
		return tx.DeleteMaintenance(t.Context(), "fixture", "missing")
	}); err != nil {
		t.Fatal(err)
	}
	assertQuiet()
	if err := s.Update(t.Context(), func(tx *Tx) error {
		if err := tx.Save(t.Context(), "fixtures", &value.Meta, &value); err != nil {
			return err
		}
		assertQuiet()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, waiter := range []<-chan struct{}{changed, other} {
		select {
		case <-waiter:
		default:
			t.Fatal("committed state did not wake every reader")
		}
	}
	var stored struct{ Name string }
	if err := s.Get(t.Context(), "fixtures", value.ID, &stored); err != nil || stored.Name != value.Name {
		t.Fatalf("notification preceded durable state: %+v %v", stored, err)
	}
	changed = s.Changes()
	assertQuiet()
}

func TestChangesObservesDirectAndTransactionalMaintenanceAndArchive(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, transaction := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "transaction"}[transaction], func(t *testing.T) {
			id := t.Name()
			changed := s.Changes()
			if transaction {
				err = s.Update(t.Context(), func(tx *Tx) error {
					return tx.SaveMaintenance(t.Context(), "fixture", id, "pending", map[string]string{"id": id})
				})
			} else {
				err = s.SaveMaintenance(t.Context(), "fixture", id, "pending", map[string]string{"id": id})
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-changed:
			default:
				t.Fatal("maintenance write was not published")
			}
			changed = s.Changes()
			if err := s.SaveMaintenance(t.Context(), "fixture", id, "pending", map[string]string{"id": id}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-changed:
				t.Fatal("unchanged maintenance created another notification")
			default:
			}
			changed = s.Changes()
			event := domain.Observation{ID: id, ConnectionID: id, ConversationID: id, ExternalEventID: id, Kind: "message.created"}
			if transaction {
				err = s.Update(t.Context(), func(tx *Tx) error {
					_, err := tx.Archive(t.Context(), event)
					return err
				})
			} else {
				_, err = s.Archive(t.Context(), event)
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-changed:
			default:
				t.Fatal("new archived message was not published")
			}
			changed = s.Changes()
			if inserted, err := s.Archive(t.Context(), event); err != nil || inserted {
				t.Fatalf("duplicate archive inserted=%v err=%v", inserted, err)
			}
			select {
			case <-changed:
				t.Fatal("duplicate archive woke readers")
			default:
			}
		})
	}
}

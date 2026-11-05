package persistence

import (
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestWatchFiltersCommittedRecordChanges(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	keys := []ChangeKey{{Kind: "notes", ID: "a"}}
	a := s.Watch(keys...)
	defer a.Close()
	keys[0].ID = "changed-by-caller"
	all := s.Watch(ChangeKey{Kind: "notes"})
	defer all.Close()
	models := s.Watch(ChangeKey{Kind: "models"})
	defer models.Close()
	check := func(ch <-chan struct{}, want bool) {
		t.Helper()
		select {
		case <-ch:
			if !want {
				t.Fatal("unrelated or uncommitted record woke watcher")
			}
		default:
			if want {
				t.Fatal("committed record did not wake watcher")
			}
		}
	}
	aChanged, allChanged, modelsChanged := a.Changes(), all.Changes(), models.Changes()
	note := struct{ domain.Meta }{Meta: domain.Meta{ID: "a"}}
	model := struct{ domain.Meta }{Meta: domain.Meta{ID: "model"}}
	if err := s.Update(t.Context(), func(tx *Tx) error {
		if err := tx.Save(t.Context(), "notes", &note.Meta, &note); err != nil {
			return err
		}
		if err := tx.Save(t.Context(), "models", &model.Meta, &model); err != nil {
			return err
		}
		check(aChanged, false)
		check(allChanged, false)
		check(modelsChanged, false)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check(aChanged, true)
	check(allChanged, true)
	check(modelsChanged, true)
	aChanged, allChanged, modelsChanged = a.Changes(), all.Changes(), models.Changes()
	other := struct{ domain.Meta }{Meta: domain.Meta{ID: "b"}}
	if err := s.Save(t.Context(), "notes", &other.Meta, &other); err != nil {
		t.Fatal(err)
	}
	check(aChanged, false)
	check(allChanged, true)
	check(modelsChanged, false)
	allChanged = all.Changes()
	// A bulk mutation without a record key must invalidate conservatively.
	if err := s.Update(t.Context(), func(tx *Tx) error {
		_, err := tx.exec(t.Context(), "DELETE FROM documents WHERE kind=? AND id=?", "notes", "b")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check(aChanged, true)
	check(allChanged, true)
	check(modelsChanged, true)
}

func TestWatchScopesMaintenanceAndConversationArchiveAndReleasesSubscriptions(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := s.Watch(ChangeKey{Kind: "im_inbox_resume", ID: "binding-a"}, ChangeKey{Kind: "observations", ID: "chat-a"})
	changed := w.Changes()
	if err := s.SaveMaintenance(t.Context(), "im_inbox_resume", "binding-b", "complete", map[string]int{"version": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Archive(t.Context(), domain.Observation{ID: "b", ConnectionID: "c", ConversationID: "chat-b", ExternalEventID: "b"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
		t.Fatal("another chat's resume or archive woke this inbox")
	default:
	}
	if err := s.SaveMaintenance(t.Context(), "im_inbox_resume", "binding-a", "complete", map[string]int{"version": 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("this binding's resume was lost")
	}
	changed = w.Changes()
	if _, err := s.Archive(t.Context(), domain.Observation{ID: "a", ConnectionID: "c", ConversationID: "chat-a", ExternalEventID: "a"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("this chat's archived message was lost")
	}
	changed = w.Changes()
	w.Close()
	w.Close()
	select {
	case <-changed:
	default:
		t.Fatal("closing watcher did not release its current waiter")
	}
	if w.Changes() != nil {
		t.Fatal("closed watcher still accepts waits")
	}
	for range 1000 {
		s.Watch(ChangeKey{Kind: "sessions", ID: domain.ID("closed-chat")}).Close()
	}
	if len(s.changes.watchers) != 0 {
		t.Fatal("closed chats retained subscriptions")
	}
}

package persistence_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestArchivePruneKeepsDedupeSequenceAndHidesBodyAcrossReopen(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	s, err := persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	event := domain.Observation{ID: "event", ConnectionID: "account", ConversationID: "chat", ExternalEventID: "external", Kind: "message.created", MessageID: "7", Text: "large archived body", ReceivedAt: time.Now().AddDate(0, 0, -100)}
	if _, err = s.Archive(ctx, event); err != nil {
		t.Fatal(err)
	}
	head, err := s.ObservationHead(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	n, _, err := s.PruneObservations(ctx, "chat", 0, head, time.Now(), func(*persistence.Tx, int, int64) error { return errors.New("fail transaction") })
	if err == nil || n != 1 {
		t.Fatal("rollback fixture", n, err)
	}
	if _, err = s.Observation(ctx, event.ID); err != nil {
		t.Fatal("rollback removed body", err)
	}
	n, _, err = s.PruneObservations(ctx, "chat", 0, head, time.Now(), nil)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if inserted, err := s.Archive(ctx, event); err != nil || inserted {
		t.Fatal("pruned event was replayed", inserted, err)
	}
	if again, err := s.ObservationHead(ctx, "chat"); err != nil || again != head {
		t.Fatal("archive watermark lost", again, err)
	}
	if _, err = s.Observation(ctx, event.ID); !domain.IsMissing(err) {
		t.Fatal("pruned payload exposed", err)
	}
	if _, err = s.Message(ctx, "chat", "7"); !domain.IsMissing(err) {
		t.Fatal("pruned message exposed", err)
	}
	rows, err := s.Observations(ctx, "chat", 0, 100)
	if err != nil || len(rows) != 0 {
		t.Fatal("receipt visible as message", rows, err)
	}
	info, err := s.ConversationArchiveInfo(ctx, "chat")
	if err != nil || info.Count != 0 || info.Bytes != 0 {
		t.Fatal("receipt counted as content", info, err)
	}
}

func TestArchivePruneResumesBatchesFromDurableCursorAndKeepsPendingMessages(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	before := now.AddDate(0, 0, -30)
	var expired []string
	var recent []string
	err = store.Update(ctx, func(tx *persistence.Tx) error {
		for i := 0; i < 530; i++ {
			id := fmt.Sprintf("event-%03d", i)
			received := now.AddDate(0, 0, -100)
			if i%53 == 0 {
				received = now.Add(-time.Hour)
				recent = append(recent, id)
			} else {
				expired = append(expired, id)
			}
			event := domain.Observation{ID: id, ConnectionID: "account", ConversationID: "chat", ExternalEventID: id, Kind: "message.created", MessageID: id, Text: "archived message body", ReceivedAt: received}
			if _, err := tx.Archive(ctx, event); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	through, err := store.ObservationHead(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	// This older message is outside the processed watermark despite its age.
	pending := domain.Observation{ID: "pending", ConnectionID: "account", ConversationID: "chat", ExternalEventID: "pending", Kind: "message.created", Text: "not processed", ReceivedAt: now.AddDate(0, 0, -100)}
	if _, err = store.Archive(ctx, pending); err != nil {
		t.Fatal(err)
	}
	other := pending
	other.ID, other.ConversationID, other.ExternalEventID = "other-chat", "other", "other-chat"
	if _, err = store.Archive(ctx, other); err != nil {
		t.Fatal(err)
	}
	type progress struct {
		domain.Meta
		After   int64 `json:"after"`
		Removed int   `json:"removed"`
	}
	current := progress{Meta: domain.Meta{ID: "cleanup"}}
	apply := func(tx *persistence.Tx, count int, next int64) error {
		current.After, current.Removed = next, current.Removed+count
		return tx.Save(ctx, "prune_progress", &current.Meta, &current)
	}
	count, next, err := store.PruneObservations(ctx, "chat", 0, through, before, apply)
	wantFirst, lookupErr := store.ObservationSequence(ctx, expired[255])
	if err != nil || lookupErr != nil || count != 256 || next != wantFirst {
		t.Fatalf("first batch: count=%d next=%d want=%d err=%v lookup=%v", count, next, wantFirst, err, lookupErr)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Get(ctx, "prune_progress", "cleanup", &current); err != nil || current.After != next || current.Removed != 256 {
		t.Fatalf("durable progress: %+v %v", current, err)
	}

	// A callback failure must roll back the bodies and its progress together.
	attempt := current
	_, failedCursor, err := store.PruneObservations(ctx, "chat", current.After, through, before, func(tx *persistence.Tx, count int, next int64) error {
		attempt.After, attempt.Removed = next, attempt.Removed+count
		if err := tx.Save(ctx, "prune_progress", &attempt.Meta, &attempt); err != nil {
			return err
		}
		return errors.New("interrupt before commit")
	})
	if err == nil || failedCursor <= current.After {
		t.Fatalf("rollback fixture: next=%d err=%v", failedCursor, err)
	}
	var persisted progress
	if err = store.Get(ctx, "prune_progress", "cleanup", &persisted); err != nil || persisted.After != current.After || persisted.Removed != 256 {
		t.Fatalf("rollback advanced progress: %+v %v", persisted, err)
	}
	if _, err = store.Observation(ctx, expired[256]); err != nil {
		t.Fatalf("rollback pruned an uncommitted message: %v", err)
	}

	count, next, err = store.PruneObservations(ctx, "chat", current.After, through, before, apply)
	wantSecond, lookupErr := store.ObservationSequence(ctx, expired[511])
	if err != nil || lookupErr != nil || count != 256 || next != wantSecond || current.Removed != 512 {
		t.Fatalf("second batch: count=%d next=%d want=%d total=%d err=%v lookup=%v", count, next, wantSecond, current.Removed, err, lookupErr)
	}
	count, next, err = store.PruneObservations(ctx, "chat", current.After, through, before, apply)
	if err != nil || count != 8 || current.Removed != 520 || next > through {
		t.Fatalf("last batch: count=%d next=%d total=%d err=%v", count, next, current.Removed, err)
	}
	count, unchanged, err := store.PruneObservations(ctx, "chat", next, through, before, nil)
	if err != nil || count != 0 || unchanged != next {
		t.Fatalf("empty batch changed cursor: %d %d %v", count, unchanged, err)
	}
	for _, id := range append(recent, pending.ID, other.ID) {
		if _, err = store.Observation(ctx, id); err != nil {
			t.Errorf("retained message %s lost: %v", id, err)
		}
	}
	info, err := store.ConversationArchiveInfo(ctx, "chat")
	if err != nil || info.Count != int64(len(recent)+1) {
		t.Fatalf("retained archive: %+v %v", info, err)
	}
	if head, err := store.ObservationHead(ctx, "chat"); err != nil || head <= through {
		t.Fatalf("pending head lost: %d %v", head, err)
	}
}

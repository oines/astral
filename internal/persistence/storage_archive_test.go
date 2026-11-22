package persistence_test

import (
	"context"
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
	n, _, err := s.PruneObservations(ctx, "chat", 0, head, time.Now())
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

func TestArchivePrunePagesEligibleBodiesAndPreservesPendingMessages(t *testing.T) {
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
	var after int64
	removed := 0
	for _, want := range []int{256, 256, 8, 0} {
		count, next, err := store.PruneObservations(ctx, "chat", after, through, before)
		if err != nil || count != want || next < after || next > through {
			t.Fatalf("batch after %d: %d, next %d, %v", after, count, next, err)
		}
		after = next
		removed += count
	}
	if removed != len(expired) {
		t.Fatalf("removed %d of %d", removed, len(expired))
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

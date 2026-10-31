package channels

import (
	"context"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func storageChat(t *testing.T) (*testkit.Platform, *Service, domain.ConversationBinding) {
	t.Helper()
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("must not run") })
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	c := bot.connection(t, s, p.Agent.ID)
	chat, err := s.ResolveConversation(p.Context, c.ID, "-100", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Bind(p.Context, chat.ID, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p, s, b
}

func TestStorageResetKeepsChatCreatesFreshPrivateSessionAndDeletesOld(t *testing.T) {
	p, s, b := storageChat(t)
	private := true
	if err := p.Harness.SaveSessionPresentation(p.Context, b.SessionID, nil, nil, nil, &private); err != nil {
		t.Fatal(err)
	}
	old, err := p.Harness.Core(p.Context, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	oldState := old.State()
	request := StorageRequest{OperationID: "cleanup_reset_1", Revision: b.Revision}
	op, err := s.StartStorageOperation(p.Context, b.ConversationID, "reset", request)
	if err != nil {
		t.Fatal(err)
	}
	s.executeStorageOperation(p.Context, op.ID)
	op, err = s.StorageOperation(p.Context, op.ID)
	if err != nil || op.State != "complete" {
		t.Fatalf("reset: %+v %v", op, err)
	}
	current, err := s.activeBinding(p.Context, b.ConversationID)
	if err != nil || current.SessionID != op.NewSessionID || current.SessionID == b.SessionID {
		t.Fatalf("binding: %+v %v", current, err)
	}
	var record domain.Session
	if err = p.Store.Get(p.Context, "sessions", b.SessionID, &record); !domain.IsMissing(err) {
		t.Fatalf("old Host Session retained: %v", err)
	}
	if _, err = p.CoreStore.LoadState(p.Context, b.SessionID); err == nil {
		t.Fatal("old Core Session retained")
	}
	page, err := p.CoreStore.Scan(p.Context, oldState.RolloutID, core.Query{Limit: 10})
	if err == nil && len(page.Records) != 0 {
		t.Fatal("old rollout retained")
	}
	if err = p.Store.Get(p.Context, "sessions", current.SessionID, &record); err != nil || !record.Private {
		t.Fatal("replacement lost privacy", err)
	}
	again, err := s.StartStorageOperation(p.Context, b.ConversationID, "reset", request)
	if err != nil || again.NewSessionID != op.NewSessionID || again.State != "complete" {
		t.Fatalf("duplicate reset: %+v %v", again, err)
	}
	request.Revision++
	if _, err = s.StartStorageOperation(p.Context, b.ConversationID, "reset", request); err == nil {
		t.Fatal("changed reuse accepted")
	}
	if p.Requests.Load() != 0 {
		t.Fatal("reset called the model")
	}
}

func TestStorageResetResumesAfterBindingCommitAndRejectsStaleBinding(t *testing.T) {
	t.Run("resume", func(t *testing.T) {
		p, s, b := storageChat(t)
		op, err := s.StartStorageOperation(p.Context, b.ConversationID, "reset", StorageRequest{OperationID: "reset_recovery", Revision: b.Revision})
		if err != nil {
			t.Fatal(err)
		}
		var old domain.Session
		if err = p.Store.Get(p.Context, "sessions", b.SessionID, &old); err != nil {
			t.Fatal(err)
		}
		if _, err = p.Harness.CreateStorageReplacement(p.Context, old, op.NewSessionID); err != nil {
			t.Fatal(err)
		}
		op.Phase = "prepared"
		if err = p.Store.Save(p.Context, storageOperations, &op.Meta, &op); err != nil {
			t.Fatal(err)
		}
		switched, err := s.bindExpected(p.Context, b.ConversationID, op.NewSessionID, nil, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		s.executeStorageOperation(p.Context, op.ID)
		result, _ := s.StorageOperation(p.Context, op.ID)
		active, _ := s.activeBinding(p.Context, b.ConversationID)
		if result.State != "complete" || active.ID != switched.ID {
			t.Fatalf("did not resume committed binding %+v %+v", result, active)
		}
	})
	t.Run("stale", func(t *testing.T) {
		p, s, b := storageChat(t)
		op, err := s.StartStorageOperation(p.Context, b.ConversationID, "reset", StorageRequest{OperationID: "reset_stale_1", Revision: b.Revision})
		if err != nil {
			t.Fatal(err)
		}
		other, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "other")
		if err != nil {
			t.Fatal(err)
		}
		next, err := s.Bind(p.Context, b.ConversationID, other.ID)
		if err != nil {
			t.Fatal(err)
		}
		s.executeStorageOperation(p.Context, op.ID)
		result, _ := s.StorageOperation(p.Context, op.ID)
		active, _ := s.activeBinding(p.Context, b.ConversationID)
		if result.State != "failed" || active.ID != next.ID {
			t.Fatal("stale reset changed latest binding")
		}
		var old domain.Session
		if err = p.Store.Get(p.Context, "sessions", b.SessionID, &old); err != nil {
			t.Fatal("old Session deleted on conflict", err)
		}
	})
}

func TestStorageArchiveProtectsPendingWatermarksAndKeepsRollout(t *testing.T) {
	p, s, b := storageChat(t)
	var chat domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", b.ConversationID, &chat); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().AddDate(0, 0, -100)
	for _, id := range []string{"processed", "pending"} {
		event := domain.Observation{ID: id, SchemaVersion: 1, ConnectionID: chat.ConnectionID, ConversationID: chat.ID, ExternalEventID: id, Kind: "message.created", MessageID: id, Text: "retained source content", ReceivedAt: oldTime, SentAt: oldTime}
		if _, err := p.Store.Archive(p.Context, event); err != nil {
			t.Fatal(err)
		}
	}
	through, err := p.Store.ObservationSequence(p.Context, "processed")
	if err != nil {
		t.Fatal(err)
	}
	c := cursor{Meta: domain.Meta{ID: b.ID}, Sequence: through}
	if err = p.Store.Save(p.Context, "inbox_cursors", &c.Meta, &c); err != nil {
		t.Fatal(err)
	}
	c = cursor{Meta: domain.Meta{ID: b.ID}, Sequence: through}
	if err = p.Store.Save(p.Context, "probe_cursors", &c.Meta, &c); err != nil {
		t.Fatal(err)
	}
	s.handoffs = map[string]int64{b.ID: through}
	before := time.Now().UTC()
	op, err := s.StartStorageOperation(p.Context, chat.ID, "archive", StorageRequest{OperationID: "archive_cleanup_1", Before: &before})
	if err != nil {
		t.Fatal(err)
	}
	s.executeStorageOperation(p.Context, op.ID)
	op, _ = s.StorageOperation(p.Context, op.ID)
	if op.State != "complete" || op.RemovedCount != 1 {
		t.Fatalf("cleanup %+v", op)
	}
	if token, err := p.Store.StorageReclaimDirty(p.Context); err != nil || token == "" {
		t.Fatal("archive cleanup committed without a durable space reclamation trigger", err)
	}
	if _, err = p.Store.Observation(p.Context, "processed"); !domain.IsMissing(err) {
		t.Fatal("processed body readable", err)
	}
	if _, err = p.Store.Observation(p.Context, "pending"); err != nil {
		t.Fatal("pending body lost", err)
	}
	if _, err = p.CoreStore.LoadState(p.Context, b.SessionID); err != nil {
		t.Fatal("archive cleanup removed rollout", err)
	}
}

func TestStorageRetentionRejectsOverlappingAndSupersededOperations(t *testing.T) {
	p, s, b := storageChat(t)
	days := 30
	first, err := s.StartStorageOperation(p.Context, b.ConversationID, "archive", StorageRequest{OperationID: "retention_first", RetentionDays: &days})
	if err != nil {
		t.Fatal(err)
	}
	forever := 0
	if _, err = s.StartStorageOperation(p.Context, b.ConversationID, "archive", StorageRequest{OperationID: "retention_second", RetentionDays: &forever}); err == nil {
		t.Fatal("overlapping archive task accepted")
	}
	s.executeStorageOperation(p.Context, first.ID)
	first, err = s.StorageOperation(p.Context, first.ID)
	if err != nil || first.State != "complete" {
		t.Fatalf("first policy: %+v %v", first, err)
	}
	// A failed task may have already committed its policy before a later step
	// failed. Retrying it must not overwrite a newer explicit policy change.
	first.State, first.Error = "failed", "interrupted after applying policy"
	if err = p.Store.Save(p.Context, storageOperations, &first.Meta, &first); err != nil {
		t.Fatal(err)
	}
	second, err := s.StartStorageOperation(p.Context, b.ConversationID, "archive", StorageRequest{OperationID: "retention_second", RetentionDays: &forever})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryStorageOperation(p.Context, first.ID); err == nil {
		t.Fatal("stale retry joined a newer queued retention change")
	}
	s.executeStorageOperation(p.Context, second.ID)
	if _, err = s.RetryStorageOperation(p.Context, first.ID); err != nil {
		t.Fatal(err)
	}
	s.executeStorageOperation(p.Context, first.ID)
	old, err := s.StorageOperation(p.Context, first.ID)
	if err != nil || old.State != "failed" {
		t.Fatalf("superseded task accepted: %+v %v", old, err)
	}
	info, err := p.Store.ConversationArchiveInfo(p.Context, b.ConversationID)
	if err != nil || info.RetentionDays != 0 {
		t.Fatalf("stale retry changed current retention: %+v %v", info, err)
	}
}

func TestStorageWorkerCancelsWhileAnotherBotOwnsCleanupLock(t *testing.T) {
	p, s, b := storageChat(t)
	op, err := s.StartStorageOperation(p.Context, b.ConversationID, "reset", StorageRequest{OperationID: "reset_cancelled_waiter", Revision: b.Revision})
	if err != nil {
		t.Fatal(err)
	}
	s.storageWorkMu.Lock()
	defer s.storageWorkMu.Unlock()
	ctx, cancel := context.WithCancel(p.Context)
	defer cancel()
	done := make(chan struct{})
	go func() { s.executeStorageOperation(ctx, op.ID); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled Bot worker cannot leave cleanup serialization wait")
	}
	active, err := s.activeBinding(p.Context, b.ConversationID)
	if err != nil || active.ID != b.ID {
		t.Fatal("cancelled waiter changed the chat binding", err)
	}
}

func TestStorageArchiveClearsProcessedPassiveMessagesWithoutProbe(t *testing.T) {
	p, s, b := storageChat(t)
	var chat domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", b.ConversationID, &chat); err != nil {
		t.Fatal(err)
	}
	chat.Kind, chat.ProactiveEnabled = "group", false
	if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
		t.Fatal(err)
	}
	event := domain.Observation{ID: "passive_archived", SchemaVersion: 1, ConnectionID: chat.ConnectionID, ConversationID: chat.ID, ExternalEventID: "passive_old", Kind: "message.created", MessageID: "passive_old", Text: "ordinary group message", ReceivedAt: time.Now().AddDate(0, 0, -100)}
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	through, err := p.Store.ObservationSequence(p.Context, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := cursor{Meta: domain.Meta{ID: b.ID}, Sequence: through}
	if err = p.Store.Save(p.Context, "inbox_cursors", &c.Meta, &c); err != nil {
		t.Fatal(err)
	}
	s.handoffs = map[string]int64{b.ID: through}
	before := time.Now().UTC()
	op, err := s.StartStorageOperation(p.Context, chat.ID, "archive", StorageRequest{OperationID: "cleanup_passive_archive", Before: &before})
	if err != nil {
		t.Fatal(err)
	}
	s.executeStorageOperation(p.Context, op.ID)
	op, err = s.StorageOperation(p.Context, op.ID)
	if err != nil || op.State != "complete" || op.RemovedCount != 1 {
		t.Fatalf("passive archive not cleaned: %+v %v", op, err)
	}
	if _, err = p.Store.Observation(p.Context, event.ID); !domain.IsMissing(err) {
		t.Fatal("processed passive body retained", err)
	}
}

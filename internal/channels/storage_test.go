package channels

import (
	"context"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
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
	request := ResetStorageRequest{SessionID: b.SessionID, Revision: b.Revision}
	replacement, err := s.ResetStorageChat(p.Context, b.ConversationID, request)
	if err != nil {
		t.Fatal(err)
	}

	current, err := s.activeBinding(p.Context, b.ConversationID)
	if err != nil || current.SessionID != replacement.SessionID || current.SessionID == b.SessionID {
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
	if _, err := s.ResetStorageChat(p.Context, b.ConversationID, request); err == nil {
		t.Fatal("stale reset of an already replaced session was accepted")
	}
	if p.Requests.Load() != 0 {
		t.Fatal("reset called the model")
	}
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
	if err = s.saveHandoffPosition(p.Context, b, through); err != nil {
		t.Fatal(err)
	}
	reclaimed := 0
	s.StorageChanged = func() { reclaimed++ }
	removed, err := s.ClearChatArchive(p.Context, chat.ID)
	if err != nil || removed != 1 || reclaimed != 1 {
		t.Fatalf("cleanup: removed %d, reclaim requests %d: %v", removed, reclaimed, err)
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

// Pause the receiving poller at the next database access after its archive
// commit. The independent cleanup can now run in the old archive-to-bind gap.
type afterArchiveContext struct {
	context.Context
	archived       <-chan struct{}
	paused, resume chan struct{}
	once           sync.Once
}

func (c *afterArchiveContext) Done() <-chan struct{} {
	select {
	case <-c.archived:
		c.once.Do(func() { close(c.paused); <-c.resume })
	default:
	}
	return c.Context.Done()
}

func TestStorageClearDuringFirstAutoBindingPreservesTrigger(t *testing.T) {
	p, s, b := storageChat(t)
	if err := s.Unbind(p.Context, b.ID, b.Revision); err != nil {
		t.Fatal(err)
	}
	var chat domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", b.ConversationID, &chat); err != nil {
		t.Fatal(err)
	}
	var account domain.Connection
	if err := p.Store.Get(p.Context, "connections", chat.ConnectionID, &account); err != nil {
		t.Fatal(err)
	}
	account.AccessMode = "all"
	if err := p.Store.Save(p.Context, "connections", &account.Meta, &account); err != nil {
		t.Fatal(err)
	}
	watch := p.Store.Watch(persistence.ChangeKey{Kind: "observations", ID: chat.ID})
	defer watch.Close()
	ctx := &afterArchiveContext{Context: p.Context, archived: watch.Changes(), paused: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(ctx.resume) })
	defer resume()
	event := domain.Observation{ID: "first-trigger", ConnectionID: account.ID, ConversationID: chat.ID, MessageID: "1", ExternalEventID: "1", Kind: "message.created", Trigger: "mention", Text: "first direct question", ReceivedAt: time.Now().UTC()}
	received := make(chan error, 1)
	go func() { received <- s.receiveObservation(ctx, account, event) }()
	select {
	case <-ctx.paused:
	case err := <-received:
		t.Fatalf("poller did not reach its archive-to-bind boundary: %v", err)
	case <-p.Context.Done():
		t.Fatal(p.Context.Err())
	}
	// While this trigger is visible but not yet bound, cleanup must be
	// unable to enter the section that reads the binding and trims its history.
	protected := !s.bindings.TryLock()
	if !protected {
		s.bindings.Unlock()
	}
	cleared := make(chan error, 1)
	go func() { _, err := s.ClearChatArchive(p.Context, chat.ID); cleared <- err }()
	resume()
	if err := <-received; err != nil {
		t.Fatal(err)
	}
	if err := <-cleared; err != nil {
		t.Fatal(err)
	}
	next, err := s.activeBinding(p.Context, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := p.Store.SearchObservations(p.Context, chat.ID, next.StartSequence, 1, "")
	if !protected {
		t.Fatal("archive cleanup could enter before the first trigger acquired its binding")
	}
	if err != nil || next.ID == "" || len(pending) != 1 || pending[0].Observation.Text != event.Text {
		t.Fatalf("first trigger lost from new binding: %+v %v", pending, err)
	}
}

func TestStorageRetentionPersistsAndRunsDaily(t *testing.T) {
	p, s, b := storageChat(t)
	if _, err := s.SetChatRetention(p.Context, b.ConversationID, 12); err == nil {
		t.Fatal("invalid retention accepted")
	}
	if _, err := s.SetChatRetention(p.Context, b.ConversationID, 30); err != nil {
		t.Fatal(err)
	}
	var policy archiveRetention
	if err := s.Store.Get(p.Context, "storage_retention", b.ConversationID, &policy); err != nil || policy.LastRun.IsZero() || policy.RetentionDays != 30 {
		t.Fatalf("retention: %+v %v", policy, err)
	}
	policy.LastRun = policy.LastRun.Add(-25 * time.Hour)
	if err := s.saveRetention(p.Context, &policy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pruneDueArchives(p.Context); err != nil {
		t.Fatal(err)
	}
	var after archiveRetention
	if err := s.Store.Get(p.Context, "storage_retention", b.ConversationID, &after); err != nil || !after.LastRun.After(policy.LastRun) {
		t.Fatal("daily retention was not applied", err)
	}
	if _, err := s.SetChatRetention(p.Context, b.ConversationID, 0); err != nil {
		t.Fatal(err)
	}
	info, err := s.Store.ConversationArchiveInfo(p.Context, b.ConversationID)
	if err != nil || info.RetentionDays != 0 {
		t.Fatal("failed to restore unlimited retention", err)
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
	if err = s.saveHandoffPosition(p.Context, b, through); err != nil {
		t.Fatal(err)
	}
	removed, err := s.ClearChatArchive(p.Context, chat.ID)
	if err != nil || removed != 1 {
		t.Fatalf("passive archive: removed %d, error %v", removed, err)
	}
	if _, err = p.Store.Observation(p.Context, event.ID); !domain.IsMissing(err) {
		t.Fatal("processed passive body retained", err)
	}
}

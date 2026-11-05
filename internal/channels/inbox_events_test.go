package channels

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

func TestInboxWaitSeesCommitBetweenReadAndWait(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	watch := store.Watch(persistence.ChangeKey{Kind: "inbox_cursors", ID: "inbox"})
	defer watch.Close()
	changed := watch.Changes()
	if _, err = store.List(t.Context(), "inbox_cursors"); err != nil {
		t.Fatal(err)
	}
	// Publish after the state read, before the worker enters its select.
	record := cursor{Meta: domain.Meta{ID: "inbox"}, Sequence: 1}
	if err = store.Save(t.Context(), "inbox_cursors", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, result := waitInbox(ctx, changed, nil, nil, time.Time{}); result || ctx.Err() != nil {
		t.Fatalf("missed committed notification: result=%v context=%v", result, ctx.Err())
	}
}

type inboxSessionPendingCounter struct {
	core.Store
	mu    sync.Mutex
	reads map[string]int
}

func (s *inboxSessionPendingCounter) Pending(ctx context.Context, id string, query core.PendingQuery) (core.Page, error) {
	s.mu.Lock()
	s.reads[id]++
	s.mu.Unlock()
	return s.Store.Pending(ctx, id, query)
}

func (s *inboxSessionPendingCounter) count(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads[id]
}

func TestInboxIgnoresAnotherChatsResumeAndArchive(t *testing.T) {
	for _, change := range []string{"resume", "archive"} {
		t.Run(change, func(t *testing.T) {
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
				t.Error("passive inbox called a model")
				return nil
			})
			counted := &inboxSessionPendingCounter{Store: p.CoreStore, reads: map[string]int{}}
			p.Harness.CoreStore = counted
			s := New(p.Store, p.Harness)
			bot := newBot(t)
			connection := bot.connection(t, s, p.Agent.ID)
			first := createBinding(t, p, s, connection, "-100", "")
			other := createBinding(t, p, s, connection, "-200", "")
			startChannels(t, s, p.Context)
			testkit.Await(t, p.Context, func() bool {
				return counted.count(first.SessionID) > 0 && counted.count(other.SessionID) > 0
			})
			// Both real inbox loops must be caught up before publishing a change.
			// The interval also exceeds the former 200ms polling period.
			time.Sleep(300 * time.Millisecond)
			firstBefore, otherBefore := counted.count(first.SessionID), counted.count(other.SessionID)
			switch change {
			case "resume":
				if err := s.ResumeIMSession(p.Context, first.SessionID); err != nil {
					t.Fatal(err)
				}
				testkit.Await(t, p.Context, func() bool { return counted.count(first.SessionID) > firstBefore })
			case "archive":
				event, _ := activeDeliveryArchive(t, p, first, "isolated-archive", "")
				testkit.Await(t, p.Context, func() bool {
					view, err := s.Delivery(p.Context, event.ID)
					return err == nil && view.State == string(core.InputApplied)
				})
			}
			time.Sleep(300 * time.Millisecond)
			if got := counted.count(other.SessionID); got != otherBefore {
				t.Fatalf("another chat's %s woke the caught-up inbox: Core Pending reads %d -> %d", change, otherBefore, got)
			}
			if counted.count(first.SessionID) <= firstBefore || p.Requests.Load() != 0 || bot.sends.Load() != 0 {
				t.Fatalf("own chat did not wake safely: reads=%d/%d model=%d send=%d", counted.count(first.SessionID), firstBefore, p.Requests.Load(), bot.sends.Load())
			}
		})
	}
}

func TestInboxWaitReceivesProbeCompletionWithoutStoreWrite(t *testing.T) {
	results := make(chan probeResult, 1)
	results <- probeResult{through: 42, participate: true}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, received := waitInbox(ctx, make(chan struct{}), nil, results, time.Time{})
	if !received || result.through != 42 || !result.participate || ctx.Err() != nil {
		t.Fatalf("lost completed Probe: %+v received=%v context=%v", result, received, ctx.Err())
	}
}

type inboxPendingCounter struct {
	core.Store
	reads atomic.Int64
}

func (s *inboxPendingCounter) Pending(ctx context.Context, id string, query core.PendingQuery) (core.Page, error) {
	s.reads.Add(1)
	return s.Store.Pending(ctx, id, query)
}

func TestInboxSleepsWhenCaughtUpAndArchiveWakesIt(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { t.Error("idle inbox called a model"); return nil })
	counted := &inboxPendingCounter{Store: p.CoreStore}
	p.Harness.CoreStore = counted
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, connection, "-100", "")
	startChannels(t, s, p.Context)
	testkit.Await(t, p.Context, func() bool { return counted.reads.Load() > 0 })
	// Startup can publish its final cursor checks. Observe a settled baseline
	// before testing that the former 200ms timer is no longer reading Core.
	time.Sleep(100 * time.Millisecond)
	before := counted.reads.Load()
	time.Sleep(450 * time.Millisecond)
	if after := counted.reads.Load(); after != before {
		t.Fatalf("caught-up inbox kept polling pending bodies: %d -> %d", before, after)
	}
	event, _ := activeDeliveryArchive(t, p, binding, "wake-after-idle", "")
	testkit.Await(t, p.Context, func() bool {
		view, err := s.Delivery(p.Context, event.ID)
		return err == nil && view.State == string(core.InputApplied)
	})
	if counted.reads.Load() <= before || p.Requests.Load() != 0 || bot.sends.Load() != 0 {
		t.Fatalf("archive did not wake passive delivery safely: reads=%d/%d model=%d send=%d", counted.reads.Load(), before, p.Requests.Load(), bot.sends.Load())
	}
}

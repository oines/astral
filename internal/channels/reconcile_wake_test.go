package channels

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

type reconcilePollAdapter struct {
	Adapter
	revision  int64
	initial   int64
	started   chan<- int64
	cancelled chan<- int64
	release   <-chan struct{}
}

func (a *reconcilePollAdapter) Poll(ctx context.Context, _ string) (Batch, error) {
	a.started <- a.revision
	<-ctx.Done()
	a.cancelled <- a.revision
	if a.revision == a.initial {
		<-a.release
	}
	return Batch{}, ctx.Err()
}

func TestReconcileWakesAfterOldPollerExits(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("configuration reconciliation ran a model")
		return nil
	})
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	initial := c.Revision
	started, cancelled := make(chan int64, 4), make(chan int64, 4)
	release := make(chan struct{})
	factory := s.Registry[c.Adapter]
	s.Registry[c.Adapter] = func(c domain.Connection, token string, client *http.Client) (Adapter, error) {
		adapter, err := factory(c, token, client)
		return &reconcilePollAdapter{Adapter: adapter, revision: c.Revision, initial: initial, started: started, cancelled: cancelled, release: release}, err
	}
	if err := s.Start(p.Context); err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			close(release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	wait := func(events <-chan int64, expected int64) {
		t.Helper()
		select {
		case got := <-events:
			if got != expected {
				t.Fatalf("poller revision=%d want=%d", got, expected)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("poller lifecycle change did not wake reconciliation")
		}
	}
	wait(started, initial)
	c.Name = "Updated connection"
	if err := p.Store.Save(p.Context, "connections", &c.Meta, &c); err != nil {
		t.Fatal(err)
	}
	wait(cancelled, initial)
	// The configuration notification has been consumed, but the old poller
	// has not exited. Its eventual exit must wake the replacement itself.
	close(release)
	released = true
	wait(started, c.Revision)
	c.Enabled = false
	if err := p.Store.Save(p.Context, "connections", &c.Meta, &c); err != nil {
		t.Fatal(err)
	}
	wait(cancelled, c.Revision-1)
	testkit.Await(t, p.Context, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.pollers) == 0
	})
}

func TestRetentionDeadlineRunsWithoutAnotherConfigurationWrite(t *testing.T) {
	p, s, binding := storageChat(t)
	policy := archiveRetention{Meta: domain.Meta{ID: binding.ConversationID}, AgentID: binding.AgentID, ConversationID: binding.ConversationID, RetentionDays: 30, LastRun: time.Now().Add(-24*time.Hour + 250*time.Millisecond)}
	if err := p.Store.Save(p.Context, "storage_retention", &policy.Meta, &policy); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(p.Context); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(p.Context, 5*time.Second)
	defer cancel()
	testkit.Await(t, ctx, func() bool {
		var current archiveRetention
		return p.Store.Get(ctx, "storage_retention", policy.ID, &current) == nil && current.LastRun.After(policy.LastRun)
	})
}

package channels

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/testkit"
)

type captionlessAdapter struct{ Adapter }

func (c captionlessAdapter) Capabilities() Capabilities {
	caps := c.Adapter.Capabilities()
	caps.CaptionLimit = 0
	return caps
}
func TestCaptionlessFileUsesSeparateOrderedReceipts(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	bound := createBinding(t, p, s, c, "-100", "")
	source, err := p.Hub.Bind(p.Context, p.Agent.ID, bound.SessionID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := p.Harness.Files.Upload(p.Context, source, "report.txt", bytes.NewBufferString("report bytes"))
	if err != nil {
		t.Fatal(err)
	}
	s.Registry[c.Adapter] = func(c domain.Connection, secret string, client *http.Client) (Adapter, error) {
		a, err := NewTelegram(c, secret, client)
		return captionlessAdapter{a}, err
	}
	request := SendRequest{IntentID: "captionless", FileURI: filestream.URL(ref), Text: "file explanation"}
	records, err := s.Enqueue(p.Context, p.Agent.ID, bound.SessionID, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Text != "file explanation" || records[0].FileRefID != "" || records[1].Text != "" || records[1].FileRefID != ref.ID || records[0].GroupID != records[1].GroupID {
		t.Fatalf("untracked or duplicated side effects: %#v", records)
	}
	repeated, err := s.Enqueue(p.Context, p.Agent.ID, bound.SessionID, request)
	if err != nil || len(repeated) != 2 || repeated[0].ID != records[0].ID || repeated[1].ID != records[1].ID {
		t.Fatalf("intent replay changed fragments: %#v %v", repeated, err)
	}
	for _, record := range records {
		if err = s.dispatch(p.Context, record.ID); err != nil {
			t.Fatal(err)
		}
		var saved domain.Outbox
		if err = p.Store.Get(p.Context, "outbox", record.ID, &saved); err != nil || saved.Status != "accepted" {
			t.Fatalf("fragment not independently receipted: %#v %v", saved, err)
		}
	}
	bot.mu.Lock()
	defer bot.mu.Unlock()
	if bot.sends.Load() != 2 || len(bot.uploads) != 1 || string(bot.uploads[0]) != "report bytes" {
		t.Fatalf("file was sent more than once: sends=%d uploads=%d", bot.sends.Load(), len(bot.uploads))
	}
}

type cursorCheckAdapter struct {
	Adapter
	poll func(context.Context, string) (Batch, error)
}

func (c cursorCheckAdapter) Poll(ctx context.Context, cursor string) (Batch, error) {
	return c.poll(ctx, cursor)
}
func TestFailedCursorCommitIsNotAcknowledged(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return nil })
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	initial := cursor{Meta: domain.Meta{ID: c.ID}, Value: "committed"}
	if err := p.Store.Save(p.Context, "channel_cursors", &initial.Meta, &initial); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(p.Context)
	defer cancel()
	done := make(chan struct{})
	calls := 0
	s.Registry[c.Adapter] = func(c domain.Connection, secret string, client *http.Client) (Adapter, error) {
		a, err := NewTelegram(c, secret, client)
		return cursorCheckAdapter{Adapter: a, poll: func(_ context.Context, value string) (Batch, error) {
			calls++
			if value != "committed" {
				t.Errorf("adapter acknowledged uncommitted cursor: %q", value)
			}
			if calls == 1 {
				// A concurrent record update makes the poller's next cursor write fail.
				if err := p.Store.Save(p.Context, "channel_cursors", &initial.Meta, &initial); err != nil {
					t.Error(err)
				}
				return Batch{NextCursor: "not-committed"}, nil
			}
			cancel()
			return Batch{}, ctx.Err()
		}}, err
	}
	go func() { defer close(done); s.poll(ctx, c) }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("poller did not retry")
	}
	if calls != 2 {
		t.Fatalf("expected retry, got %d calls", calls)
	}
}

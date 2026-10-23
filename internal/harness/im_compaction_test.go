package harness

import (
	"context"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestCompactionPromptFollowsCurrentIMBinding(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		return textReply("confirmed context")
	})
	// Seven large-history compactions exceed the shared fixture's 35 second
	// deadline under the race detector. Keep the assertions and request sizes.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f.ctx = ctx
	compactPrompt := func(id string) string {
		t.Helper()
		if _, err := f.service.Submit(f.ctx, id, InputRequest{ID: domain.ID("input"), Text: strings.Repeat("conversation context ", 5000)}); err != nil {
			t.Fatal(err)
		}
		f.idle(t, id)
		if err := f.service.Compact(f.ctx, id, ""); err != nil {
			t.Fatal(err)
		}
		session, err := f.service.Core(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		page, err := session.History().Scan(f.ctx, core.Query{Kind: core.EntryModelRequest, Reverse: true, Limit: 1, MaxBytes: 8 << 20})
		if err != nil || len(page.Entries) != 1 || page.Entries[0].ModelRequest.Purpose != "compaction" {
			t.Fatalf("missing actual compaction request: %v", err)
		}
		return page.Entries[0].ModelRequest.Request.SystemPrompt
	}
	setBindings := func(bindings map[string]string) {
		t.Helper()
		ids := make([]string, 0, len(bindings))
		for id := range bindings {
			ids = append(ids, id)
		}
		// Keep the message-tool definition identical across binding states so
		// changing the compaction prompt alone must refresh an open runtime.
		if err := f.service.WithStoppedSessions(f.ctx, ids, func() error {
			return f.store.Update(f.ctx, func(tx *persistence.Tx) error {
				for id, binding := range bindings {
					var record domain.Session
					if err := tx.Get(f.ctx, "sessions", id, &record); err != nil {
						return err
					}
					record.BindingID, record.ConversationID = binding, ""
					if binding != "" {
						record.ConversationID = "conversation-" + binding
					}
					if err := tx.Save(f.ctx, "sessions", &record.Meta, &record); err != nil {
						return err
					}
				}
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(label, id, want string) {
		t.Helper()
		if got := compactPrompt(id); got != want {
			t.Fatalf("%s: compaction did not use the current binding's prompt", label)
		}
	}

	defaultPrompt := compactPrompt(f.session.ID)
	if defaultPrompt == "" || defaultPrompt == imCompactionPrompt {
		t.Fatal("unbound Session did not inherit the Core default")
	}
	setBindings(map[string]string{f.session.ID: "first"})
	check("binding an open Session", f.session.ID, imCompactionPrompt)
	f.restart(t)
	check("reopening a bound Session", f.session.ID, imCompactionPrompt)

	parent, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.service.Fork(f.ctx, f.session.ID, parent.Head(), "unbound copy", domain.ID("session"))
	if err != nil {
		t.Fatal(err)
	}
	check("unbound fork of an IM Session", child.ID, defaultPrompt)
	setBindings(map[string]string{f.session.ID: "", child.ID: "replacement"})
	check("previous Session after rebinding", f.session.ID, defaultPrompt)
	check("replacement bound Session", child.ID, imCompactionPrompt)
	setBindings(map[string]string{child.ID: ""})
	check("unbinding an open Session", child.ID, defaultPrompt)
}

package channels

import (
	"context"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

func TestBindingArchivedSessionRequiresExplicitRestore(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("must not run") })
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	archived := true
	if err := p.Harness.SaveSessionPresentation(p.Context, p.Session.ID, nil, nil, &archived, nil); err != nil {
		t.Fatal(err)
	}
	var before domain.Session
	if err := p.Store.Get(p.Context, "sessions", p.Session.ID, &before); err != nil || !before.Archived {
		t.Fatalf("ordinary Session was not archived: %+v %v", before, err)
	}
	conversation, err := s.ResolveConversation(p.Context, connection.ID, "-100", "")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.Bind(p.Context, conversation.ID, before.ID)
	if !persistence.IsSessionArchived(err) {
		t.Fatalf("binding implicitly restored a sealed Session: %+v %v", binding, err)
	}
	if _, loaded := p.Harness.IMProbeRecoveryPossible(before.ID); loaded {
		t.Fatal("rejected binding opened sealed history")
	}
	var unchanged domain.Session
	if err := p.Store.Get(p.Context, "sessions", before.ID, &unchanged); err != nil || unchanged.Revision != before.Revision || !unchanged.Archived || unchanged.BindingID != "" {
		t.Fatalf("rejected binding changed sealed Session: %+v %v", unchanged, err)
	}
	archived = false
	if err := p.Harness.SaveSessionPresentation(p.Context, before.ID, nil, nil, &archived, nil); err != nil {
		t.Fatal(err)
	}
	var restored domain.Session
	if err := p.Store.Get(p.Context, "sessions", before.ID, &restored); err != nil {
		t.Fatal(err)
	}
	binding, err = s.Bind(p.Context, conversation.ID, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	var after, previous domain.Session
	if err := p.Store.Get(p.Context, "sessions", before.ID, &after); err != nil {
		t.Fatal(err)
	}
	if after.Archived || after.BindingID != binding.ID || after.ConversationID != conversation.ID || after.Title != conversation.Title || after.Revision != restored.Revision+1 {
		t.Fatalf("binding did not attach the explicitly restored Session: %+v", after)
	}
	if err := p.Store.Version(p.Context, "sessions", before.ID, before.Revision, &previous); err != nil || !previous.Archived || previous.BindingID != "" {
		t.Fatalf("binding rewrote its previous archived revision: %+v %v", previous, err)
	}
	if p.Requests.Load() != 0 {
		t.Fatal("binding and unarchiving unexpectedly invoked a model")
	}
}

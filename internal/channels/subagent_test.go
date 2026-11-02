package channels

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestSubagentsKeepChatReadsButCannotBindOrSend(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	account := bot.connection(t, s, p.Agent.ID)
	account.AccessMode = "all"
	if err := s.SaveConnection(p.Context, &account, nil); err != nil {
		t.Fatal(err)
	}
	chat, err := s.ResolveForAgent(p.Context, p.Agent.ID, account.ID, "-100", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "Internal work")
	if err != nil {
		t.Fatal(err)
	}
	child.SubagentParentID = p.Session.ID
	if err := p.Store.Save(p.Context, "sessions", &child.Meta, &child); err != nil {
		t.Fatal(err)
	}
	binding := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: child.ID}
	for name, write := range map[string]func() error{
		"IM binding": func() error { _, err := s.Bind(p.Context, chat.ID, child.ID); return err },
		"IM background": func() error {
			return s.deliver(p.Context, domain.ConversationBinding{AgentID: p.Agent.ID, SessionID: child.ID, ConversationID: chat.ID}, domain.Observation{ID: "new-im-background", ConversationID: chat.ID})
		},
		"outbox": func() error {
			_, err := s.Enqueue(p.Context, p.Agent.ID, child.ID, SendRequest{IntentID: "child-send", ConversationID: chat.ID, Text: "do not send"})
			return err
		},
		"Telegram send": func() error {
			_, err := s.TelegramOperation(p.Context, binding, account.ID, "sendMessage", TelegramRequest{ConversationID: chat.ID, Parameters: map[string]any{"text": "do not send"}})
			return err
		},
		"forwarding": func() error {
			_, err := s.AccountOperation(p.Context, binding, account.ID, "forwardMessage", ChatOperationRequest{ConversationID: chat.ID, SourceConversationID: chat.ID, Parameters: map[string]any{"message_id": 1}})
			return err
		},
		"reaction": func() error {
			_, err := s.TelegramOperation(p.Context, binding, account.ID, "setMessageReaction", TelegramRequest{ConversationID: chat.ID, Parameters: map[string]any{"message_id": 1}})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			var problem *domain.Error
			if err := write(); !errors.As(err, &problem) || problem.Status != http.StatusForbidden {
				t.Fatalf("child escaped its parent through %s: %v", name, err)
			}
		})
	}
	if _, err := s.TelegramOperation(p.Context, binding, account.ID, "getChat", TelegramRequest{ConversationID: chat.ID}); err != nil {
		t.Fatalf("child lost authorized platform reads: %v", err)
	}
	if _, err := s.AccountOperation(p.Context, binding, account.ID, "getChat", ChatOperationRequest{ConversationID: chat.ID}); err != nil {
		t.Fatalf("child lost account operation reads: %v", err)
	}
	if bot.sends.Load() != 0 || len(bot.operations) != 0 {
		t.Fatal("rejected subagent write reached the platform")
	}
	for _, kind := range []string{"outbox", "bindings"} {
		rows, err := p.Store.List(p.Context, kind)
		if err != nil || len(rows) != 0 {
			t.Fatalf("rejected child writes left %s state: count=%d error=%v", kind, len(rows), err)
		}
	}
	if _, err := p.Harness.Receipt(p.Context, child.ID, "new-im-background"); !domain.IsMissing(err) {
		t.Fatalf("new IM background was accepted by the child: %v", err)
	}
	// Restoring a pending outbox entry cannot bypass the identity check at
	// dispatch, even when no new Enqueue request is made.
	queued := domain.Outbox{Meta: domain.Meta{ID: "imported-child-send"}, AgentID: p.Agent.ID, SessionID: child.ID, ConversationID: chat.ID, Status: "pending"}
	if err := p.Store.Save(p.Context, "outbox", &queued.Meta, &queued); err != nil {
		t.Fatal(err)
	}
	if err := s.dispatch(p.Context, queued.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.Get(p.Context, "outbox", queued.ID, &queued); err != nil || queued.Status != "blocked" || bot.sends.Load() != 0 {
		t.Fatalf("pending child send bypassed dispatch authority: %+v %v", queued, err)
	}
}

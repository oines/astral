package channels

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

func TestAllChatActivationHasNoHistoryPrefillAndModesKeepBindings(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encoded(request["messages"])), "before activation") {
			t.Error("first activation replayed old messages")
		}
		return testkit.Text("Web only")
	})
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	c.AccessMode = "all"
	if err := s.SaveConnection(p.Context, &c, nil); err != nil {
		t.Fatal(err)
	}
	chat, err := s.ResolveForAgent(p.Context, p.Agent.ID, c.ID, "-100", "")
	if err != nil {
		t.Fatal(err)
	}
	if bindings, _ := list[domain.ConversationBinding](p.Context, p.Store, "bindings"); len(bindings) != 0 {
		t.Fatal("API resolution created a Session")
	}
	startChannels(t, s, p.Context)
	bot.updates <- []telegramUpdate{update(1, "before activation")}
	testkit.Await(t, p.Context, func() bool { _, err := p.Store.Message(p.Context, chat.ID, "101"); return err == nil })
	if bindings, _ := list[domain.ConversationBinding](p.Context, p.Store, "bindings"); len(bindings) != 0 {
		t.Fatal("ordinary group message activated all-mode chat")
	}
	trigger := update(2, "@astral_bot hello")
	trigger.Message.Entities = []telegramEntity{{Type: "mention", Offset: 0, Length: 11}}
	bot.updates <- []telegramUpdate{trigger, trigger}
	var b domain.ConversationBinding
	testkit.Await(t, p.Context, func() bool { b, _ = s.activeBinding(p.Context, chat.ID); return b.ID != "" })
	p.Idle(t, b.SessionID)
	testkit.Await(t, p.Context, func() bool { return p.Requests.Load() == 1 })
	p.Idle(t, b.SessionID)
	all, _ := list[domain.ConversationBinding](p.Context, p.Store, "bindings")
	if len(all) != 1 {
		t.Fatalf("activation created duplicate bindings: %d", len(all))
	}
	sequence, _ := p.Store.ObservationSequence(p.Context, stableID("event", c.ID, "2"))
	if b.StartSequence != sequence-1 {
		t.Fatalf("activation did not start at trigger: %+v", b)
	}
	if err := p.Store.Get(p.Context, "conversations", chat.ID, &chat); err != nil {
		t.Fatal(err)
	}
	chat, err = s.SetProactive(p.Context, chat.ID, chat.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"specified", "all", "specified"} {
		c.AccessMode = mode
		if err := s.SaveConnection(p.Context, &c, nil); err != nil {
			t.Fatal(err)
		}
		now, _ := s.activeBinding(p.Context, chat.ID)
		if now.ID != b.ID {
			t.Fatal("scope switch replaced Session")
		}
	}
	changed := chat
	changed.Title = "Renamed group"
	changed.ProactiveEnabled = false
	if err := s.saveConversation(p.Context, &changed); err != nil {
		t.Fatal(err)
	}
	if !changed.ProactiveEnabled {
		t.Fatal("platform update lost proactive setting")
	}
	var session domain.Session
	_ = p.Store.Get(p.Context, "sessions", b.SessionID, &session)
	if session.Title != changed.Title || session.ChannelPlatform != "telegram" || session.TitleSource != "conversation" {
		t.Fatalf("bound presentation=%+v", session)
	}
	next, err := s.Bind(p.Context, chat.ID, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.SessionID != p.Session.ID {
		t.Fatal("existing Session was not selected")
	}
	session = domain.Session{}
	_ = p.Store.Get(p.Context, "sessions", b.SessionID, &session)
	if session.BindingID != "" || session.ChannelPlatform != "" {
		t.Fatal("old Session stayed bound")
	}
	other, err := s.ResolveConversation(p.Context, c.ID, "-200", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Bind(p.Context, other.ID, p.Session.ID); err == nil {
		t.Fatal("one Session was bound to two chats")
	}
	if err = s.Unbind(p.Context, next.ID, next.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.AuthorizeConversation(p.Context, p.Agent.ID, chat.ID); err == nil {
		t.Fatal("removed chat still authorized")
	}
}

func TestRebindStopsOriginalAndTargetBeforeCommit(t *testing.T) {
	entered := make(chan struct{}, 2)
	p := testkit.New(t, 0, func(ctx context.Context, _ int, _ map[string]any) any {
		entered <- struct{}{}
		<-ctx.Done()
		return nil
	})
	bot := newBot(t)
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	b := createBinding(t, p, s, c, "-100", "")
	for _, id := range []string{b.SessionID, p.Session.ID} {
		if _, err := p.Harness.Submit(p.Context, id, harness.InputRequest{ID: "run:" + id, Text: "continue working"}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case <-entered:
		case <-p.Context.Done():
			t.Fatal(p.Context.Err())
		}
	}
	next, err := s.Bind(p.Context, b.ConversationID, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{b.SessionID, p.Session.ID} {
		view, err := p.Harness.View(p.Context, id)
		if err != nil {
			t.Fatal(err)
		}
		if view.RunID != "" {
			t.Fatalf("binding committed while Session %s ran", id)
		}
	}
	if next.SessionID != p.Session.ID {
		t.Fatal("target lost")
	}
}

func TestPrivateFirstMessageActivatesAndDirectMessagesBypassProbe(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(_ context.Context, _ int, r map[string]any) any {
		if strings.Contains(string(encoded(r["tools"])), `"name":"decide"`) {
			t.Error("direct message used Probe")
		}
		return testkit.Text("Web only")
	})
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	c.AccessMode = "all"
	if err := s.SaveConnection(p.Context, &c, nil); err != nil {
		t.Fatal(err)
	}
	startChannels(t, s, p.Context)
	event := update(1, "hello privately")
	event.Message.Chat = telegramChat{ID: 7, Type: "private", FirstName: "Ada"}
	bot.updates <- []telegramUpdate{event}
	testkit.Await(t, p.Context, func() bool { return p.Requests.Load() == 1 })
	b, err := s.activeBinding(p.Context, stableID("conversation", c.ID, "7", ""))
	if err != nil || b.ID == "" {
		t.Fatalf("private chat did not activate: %v", err)
	}
	p.Idle(t, b.SessionID)
	if bot.sends.Load() != 0 {
		t.Fatal("assistant text was automatically sent")
	}
	ctx, cancel := context.WithTimeout(p.Context, time.Second)
	defer cancel()
	if err := s.Unbind(ctx, b.ID, b.Revision); err == nil {
		t.Fatal("all-mode chat removed without switching scope")
	}
	c.Enabled = false
	if err := s.SaveConnection(p.Context, &c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveForAgent(p.Context, p.Agent.ID, c.ID, "-200", ""); err == nil {
		t.Fatal("disabled account still resolved a chat through the Computer API")
	}
}

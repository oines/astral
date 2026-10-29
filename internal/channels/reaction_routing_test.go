package channels

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

func reactionRoutingFixture(t *testing.T) (*testkit.Platform, *Service, domain.Connection, *telegram) {
	t.Helper()
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	service := New(p.Store, p.Harness)
	connection := newBot(t).connection(t, service, p.Agent.ID)
	adapter, err := service.adapter(p.Context, connection)
	if err != nil {
		t.Fatal(err)
	}
	return p, service, connection, adapter.(*telegram)
}

func reactionRoutingArchive(t *testing.T, p *testkit.Platform, connectionID, conversationID, messageID string) {
	t.Helper()
	observation := domain.Observation{
		SchemaVersion: 1, ID: domain.ID("original"), ExternalEventID: domain.ID("external"),
		ConnectionID: connectionID, ConversationID: conversationID, MessageID: messageID,
		Kind: "message.created", Text: "Archived source message", SentAt: time.Now(), ReceivedAt: time.Now(),
	}
	if _, err := p.Store.Archive(p.Context, observation); err != nil {
		t.Fatal(err)
	}
}

func reactionRoutingEvent(adapter *telegram, chatID, messageID int64) Event {
	return adapter.normalizeReaction(50, telegramMessageReaction{
		Chat: telegramChat{ID: chatID, Type: "supergroup", IsForum: true}, MessageID: messageID,
		User: &telegramUser{ID: 7, FirstName: "Ada"}, Date: time.Now().Unix(),
		NewReaction: []telegramReactionType{{Type: "emoji", Emoji: "👍"}},
	})[0]
}

func TestReactionRoutingUsesArchivedTopicAndCurrentAccountPermissions(t *testing.T) {
	t.Run("known source selects only its Topic", func(t *testing.T) {
		p, service, connection, adapter := reactionRoutingFixture(t)
		first := createBinding(t, p, service, connection, "-100", "22")
		second := createBinding(t, p, service, connection, "-100", "33")
		reactionRoutingArchive(t, p, connection.ID, first.ConversationID, "101")
		event, allowed, err := service.routeReaction(p.Context, connection.ID, reactionRoutingEvent(adapter, -100, 101))
		if err != nil || !allowed || event.Conversation.ID != first.ConversationID || event.Conversation.TopicID != "22" || event.Observation.ConversationID != first.ConversationID || event.Observation.ConversationID == second.ConversationID {
			t.Fatalf("reaction was not confined to its archived Topic: %+v allowed=%v error=%v", event, allowed, err)
		}
	})
	t.Run("Topic-only unknown source cannot select any Topic", func(t *testing.T) {
		p, service, connection, adapter := reactionRoutingFixture(t)
		createBinding(t, p, service, connection, "-100", "22")
		createBinding(t, p, service, connection, "-100", "33")
		if _, allowed, err := service.routeReaction(p.Context, connection.ID, reactionRoutingEvent(adapter, -100, 999)); err != nil || allowed {
			t.Fatalf("unknown source bypassed Topic-only permissions: allowed=%v error=%v", allowed, err)
		}
	})
	for _, mode := range []string{"parent grant", "all chats"} {
		t.Run("unknown source with "+mode, func(t *testing.T) {
			p, service, connection, adapter := reactionRoutingFixture(t)
			if mode == "parent grant" {
				createBinding(t, p, service, connection, "-100", "")
			} else {
				connection.AccessMode = "all"
				if err := service.SaveConnection(p.Context, &connection, nil); err != nil {
					t.Fatal(err)
				}
			}
			incoming := reactionRoutingEvent(adapter, -100, 999)
			event, allowed, err := service.routeReaction(p.Context, connection.ID, incoming)
			if err != nil || !allowed || event.Conversation.ID != incoming.Conversation.ID || event.Conversation.TopicID != "" || event.Observation.ConversationID != event.Conversation.ID {
				t.Fatalf("authorized parent fallback failed: %+v allowed=%v error=%v", event, allowed, err)
			}
		})
	}
	t.Run("archived ungranted chat is still forbidden", func(t *testing.T) {
		p, service, connection, adapter := reactionRoutingFixture(t)
		createBinding(t, p, service, connection, "-100", "22")
		foreign, err := service.ResolveConversation(p.Context, connection.ID, "-200", "")
		if err != nil {
			t.Fatal(err)
		}
		reactionRoutingArchive(t, p, connection.ID, foreign.ID, "101")
		if _, allowed, err := service.routeReaction(p.Context, connection.ID, reactionRoutingEvent(adapter, -200, 101)); err != nil || allowed {
			t.Fatalf("archival membership became an access grant: allowed=%v error=%v", allowed, err)
		}
	})
	t.Run("parent grant does not bypass a known ungranted Topic", func(t *testing.T) {
		p, service, connection, adapter := reactionRoutingFixture(t)
		createBinding(t, p, service, connection, "-100", "")
		topic, err := service.ResolveConversation(p.Context, connection.ID, "-100", "22")
		if err != nil {
			t.Fatal(err)
		}
		reactionRoutingArchive(t, p, connection.ID, topic.ID, "101")
		if _, allowed, err := service.routeReaction(p.Context, connection.ID, reactionRoutingEvent(adapter, -100, 101)); err != nil || allowed {
			t.Fatalf("known Topic escaped to a granted parent: allowed=%v error=%v", allowed, err)
		}
	})
	for _, change := range []string{"revoke binding", "disable account"} {
		t.Run(change, func(t *testing.T) {
			p, service, connection, adapter := reactionRoutingFixture(t)
			binding := createBinding(t, p, service, connection, "-100", "22")
			reactionRoutingArchive(t, p, connection.ID, binding.ConversationID, "101")
			incoming := reactionRoutingEvent(adapter, -100, 101)
			if _, allowed, err := service.routeReaction(p.Context, connection.ID, incoming); err != nil || !allowed {
				t.Fatalf("initial grant was not usable: allowed=%v error=%v", allowed, err)
			}
			if change == "revoke binding" {
				if err := service.Unbind(p.Context, binding.ID, binding.Revision); err != nil {
					t.Fatal(err)
				}
			} else {
				connection.Enabled = false
				if err := service.SaveConnection(p.Context, &connection, nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, allowed, err := service.routeReaction(p.Context, connection.ID, incoming); err != nil || allowed {
				t.Fatalf("stale adapter retained removed permissions: allowed=%v error=%v", allowed, err)
			}
		})
	}
	t.Run("cross-account event identities are rejected", func(t *testing.T) {
		p, service, connection, adapter := reactionRoutingFixture(t)
		binding := createBinding(t, p, service, connection, "-100", "22")
		reactionRoutingArchive(t, p, connection.ID, binding.ConversationID, "101")
		for _, field := range []string{"observation", "conversation"} {
			event := reactionRoutingEvent(adapter, -100, 101)
			if field == "observation" {
				event.Observation.ConnectionID = "another-account"
			} else {
				event.Conversation.ConnectionID = "another-account"
			}
			if _, allowed, err := service.routeReaction(p.Context, connection.ID, event); err == nil || allowed {
				t.Fatalf("forged %s account identity was accepted: allowed=%v error=%v", field, allowed, err)
			}
		}
	})
	t.Run("ambiguous source cannot choose a Topic", func(t *testing.T) {
		p, service, connection, adapter := reactionRoutingFixture(t)
		first := createBinding(t, p, service, connection, "-100", "22")
		second := createBinding(t, p, service, connection, "-100", "33")
		reactionRoutingArchive(t, p, connection.ID, first.ConversationID, "101")
		reactionRoutingArchive(t, p, connection.ID, second.ConversationID, "101")
		if _, allowed, err := service.routeReaction(p.Context, connection.ID, reactionRoutingEvent(adapter, -100, 101)); err != nil || allowed {
			t.Fatalf("ambiguous source was accepted or stalled polling: allowed=%v error=%v", allowed, err)
		}
	})
	t.Run("reaction alone does not prove Topic membership", func(t *testing.T) {
		p, service, connection, adapter := reactionRoutingFixture(t)
		binding := createBinding(t, p, service, connection, "-100", "22")
		old := reactionRoutingEvent(adapter, -100, 101).Observation
		old.ConversationID = binding.ConversationID
		if _, err := p.Store.Archive(p.Context, old); err != nil {
			t.Fatal(err)
		}
		if _, allowed, err := service.routeReaction(p.Context, connection.ID, reactionRoutingEvent(adapter, -100, 101)); err != nil || allowed {
			t.Fatalf("a prior reaction was treated as original message membership: allowed=%v error=%v", allowed, err)
		}
	})
}

func TestTelegramReactionPollDeliveryStaysInSourceTopicAndWorkingContext(t *testing.T) {
	bot := newBot(t)
	firstEntered, secondEntered := make(chan struct{}), make(chan struct{})
	firstGate, releaseFirst := activeDeliveryGate(t)
	secondGate, releaseSecond := activeDeliveryGate(t)
	defer releaseFirst()
	defer releaseSecond()
	requests := make(chan map[string]any, 4)
	var mains, probes atomic.Int64
	p := testkit.New(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encoded(request["tools"])), `"name":"decide"`) {
			probes.Add(1)
			return testkit.Tool("decide", map[string]bool{"participate": true})
		}
		requests <- request
		switch mains.Add(1) {
		case 1:
			close(firstEntered)
			select {
			case <-firstGate:
			case <-ctx.Done():
				return nil
			}
			// A rejected command gives a real tool boundary without a Computer
			// or shell process, as in the active-delivery regression tests.
			return testkit.Tool("exec_command", map[string]any{"cmd": "fixture must never execute", "login": false})
		case 2:
			close(secondEntered)
			select {
			case <-secondGate:
			case <-ctx.Done():
				return nil
			}
		}
		return testkit.Text("Finished without an IM send.")
	})
	service := New(p.Store, p.Harness)
	service.probeQuiet, service.probeMaxWait = time.Millisecond, 2*time.Millisecond
	connection := bot.connection(t, service, p.Agent.ID)
	first := createBinding(t, p, service, connection, "-100", "22")
	second := createBinding(t, p, service, connection, "-100", "33")
	var chat domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", first.ConversationID, &chat); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetProactive(p.Context, chat.ID, chat.Revision, true); err != nil {
		t.Fatal(err)
	}
	startChannels(t, service, p.Context)
	if _, err := p.Harness.Submit(p.Context, first.SessionID, harness.InputRequest{ID: "start-reaction-work", Text: "Continue existing work."}); err != nil {
		t.Fatal(err)
	}
	activeDeliveryWait(t, p.Context, firstEntered, "working Agent's first request")
	message := update(1, "Unique source text for reaction routing")
	message.Message.IsTopicMessage, message.Message.ThreadID = true, 22
	reaction := telegramMessageReaction{
		Chat: message.Message.Chat, MessageID: message.Message.ID, Date: time.Now().Unix(),
		User: &telegramUser{ID: 8, FirstName: "Bob"}, NewReaction: []telegramReactionType{{Type: "emoji", Emoji: "👍"}},
	}
	bot.updates <- []telegramUpdate{message, {ID: 2, MessageReaction: &reaction}}
	messageEventID := stableID("event", connection.ID, "1")
	reactionEventID := stableID("event", connection.ID, "2:reaction:added")
	activeDeliveryRouted(t, p, messageEventID)
	activeDeliveryRouted(t, p, reactionEventID)
	if events, err := p.Store.Observations(p.Context, second.ConversationID, 0, 100); err != nil || len(events) != 0 {
		t.Fatalf("reaction was broadcast into another Topic: %+v %v", events, err)
	}
	if observations, err := p.Store.Observations(p.Context, first.ConversationID, 0, 100); err != nil || len(observations) != 2 || observations[1].Observation.Kind != "message.reaction.added" || observations[1].Observation.Trigger != "" {
		t.Fatalf("polling did not archive the message and passive reaction: %+v %v", observations, err)
	}
	releaseFirst()
	activeDeliveryWait(t, p.Context, secondEntered, "next main request with reaction")
	firstRequest, secondRequest := <-requests, <-requests
	if strings.Contains(activeDeliveryText(firstRequest), "Unique source text") || strings.Contains(activeDeliveryText(firstRequest), "message.reaction.added") {
		t.Fatal("new message or reaction changed an already frozen request")
	}
	text := activeDeliveryText(secondRequest)
	if strings.Count(text, "Unique source text for reaction routing") != 1 || strings.Count(text, `kind="message.reaction.added"`) != 1 || !strings.Contains(text, `conversation-id="`+first.ConversationID+`"`) {
		t.Fatalf("working Agent did not receive exactly one source message and reaction: %s", text)
	}
	if strings.Contains(text, "<im_trigger") || strings.Contains(text, "<im_wakeup") || probes.Load() != 0 {
		t.Fatalf("reaction caused an extra IM driver or Probe: probes=%d context=%s", probes.Load(), text)
	}
	releaseSecond()
	p.Idle(t, first.SessionID)
	adapter, err := service.adapter(p.Context, connection)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.archiveTelegramResult(p.Context, adapter.(*telegram), chat, "setMessageReaction", map[string]any{"message_id": "101", "reaction": []any{map[string]any{"type": "emoji", "emoji": "❤"}}}, json.RawMessage("true")); err != nil {
		t.Fatal(err)
	}
	observations, err := p.Store.Observations(p.Context, first.ConversationID, 0, 100)
	if err != nil || len(observations) != 3 {
		t.Fatalf("accepted self reaction was not appended: %+v %v", observations, err)
	}
	self := observations[2]
	activeDeliveryRouted(t, p, self.Observation.ID)
	if !self.Observation.Self || self.Observation.Trigger != "" || self.Observation.Kind != "message.reaction.set" {
		t.Fatalf("self operation acquired an inbound trigger: %+v", self)
	}
	if window, err := service.pendingProbe(p.Context, first, self.Sequence, time.Now().Add(time.Minute)); err != nil || window.Through != 0 {
		t.Fatalf("consumed or self reaction remained eligible for a new Probe: %+v %v", window, err)
	}
	if mains.Load() != 2 || probes.Load() != 0 {
		t.Fatalf("passive reactions caused extra model work: main=%d Probe=%d", mains.Load(), probes.Load())
	}
	if original, err := p.Store.Message(p.Context, first.ConversationID, "101"); err != nil || original.Kind != "message.created" || original.Text != message.Message.Text {
		t.Fatalf("reaction replaced the source message read: %+v %v", original, err)
	}
}

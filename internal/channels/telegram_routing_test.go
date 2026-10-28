package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestTelegramTopicRoutingRequiresExplicitTopicMessage(t *testing.T) {
	for _, test := range []struct {
		name, chat, flag, topic, kind, trigger string
	}{
		{"ordinary group reply", `{"id":-100,"type":"supergroup"}`, "", "", "group", "reply"},
		{"forum ordinary reply", `{"id":-100,"type":"supergroup","is_forum":true}`, `,"is_topic_message":false`, "", "group", "reply"},
		{"forum topic", `{"id":-100,"type":"supergroup","is_forum":true}`, `,"is_topic_message":true`, "42", "topic", "reply"},
		{"private ordinary reply", `{"id":7,"type":"private"}`, "", "", "private", "private"},
		{"private bot topic", `{"id":7,"type":"private"}`, `,"is_topic_message":true`, "42", "topic", "private"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var message telegramMessage
			raw := fmt.Sprintf(`{"message_id":43,"message_thread_id":42%s,"chat":%s,"text":"reply","from":{"id":7},"reply_to_message":{"message_id":42,"from":{"id":999},"text":"original"}}`, test.flag, test.chat)
			if err := json.Unmarshal([]byte(raw), &message); err != nil {
				t.Fatal(err)
			}
			adapter := &telegram{connection: domain.Connection{Meta: domain.Meta{ID: "account"}, AccountID: "999"}}
			event := adapter.normalize(1, message, false)
			wantID := stableID("conversation", "account", strconv.FormatInt(message.Chat.ID, 10), test.topic)
			if event.Conversation.ID != wantID || event.Observation.ConversationID != wantID || event.Conversation.TopicID != test.topic || event.Conversation.Kind != test.kind {
				t.Fatalf("message thread changed conversation routing: %+v", event.Conversation)
			}
			if event.Observation.Trigger != test.trigger || event.Observation.ReplyID != "42" {
				t.Fatal("routing changed the reply identity or trigger")
			}
		})
	}
}

func TestTelegramReplyThreadsDeliverToExistingGroupSession(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 1, func(_ context.Context, n int, request map[string]any) any {
		if n == 1 && !strings.Contains(fmt.Sprint(request["messages"]), `<quote position-utf16="3" manual="true">selected &lt;source&gt;&amp;</quote>`) {
			t.Error("selected quote did not reach the model as escaped observation content")
		}
		return testkit.Text("received")
	})
	service := New(p.Store, p.Harness)
	connection := bot.connection(t, service, p.Agent.ID)
	group := createBinding(t, p, service, connection, "-100", "")
	topic := createBinding(t, p, service, connection, "-100", "77")
	startChannels(t, service, p.Context)
	for i, test := range []struct {
		thread, sender int64
		text           string
		isTopic        bool
		binding        domain.ConversationBinding
	}{
		{900, 999, "reply to bot", false, group},
		{901, 7, "@astral_bot reply to another member", false, group},
		{77, 999, "real topic reply", true, topic},
		{902, 999, "another ordinary group reply", false, group},
	} {
		incoming := update(int64(i+1), test.text)
		incoming.Message.ThreadID = test.thread
		incoming.Message.IsTopicMessage = test.isTopic
		incoming.Message.Chat.IsForum = test.isTopic
		incoming.Message.Reply = &telegramMessage{ID: test.thread, From: &telegramUser{ID: test.sender}, Text: "original"}
		if i == 0 {
			incoming.Message.Reply.Text = "😀xselected <source>& original body"
			incoming.Message.Quote = &telegramTextQuote{Text: "selected <source>&", Position: 3, IsManual: true}
		}
		if strings.HasPrefix(test.text, "@astral_bot") {
			incoming.Message.Entities = []telegramEntity{{Type: "mention", Length: 11}}
		}
		bot.updates <- []telegramUpdate{incoming}
		eventID := stableID("event", connection.ID, strconv.Itoa(i+1))
		testkit.Await(t, p.Context, func() bool {
			delivery, err := service.Delivery(p.Context, eventID)
			return err == nil && delivery.SessionID == test.binding.SessionID && delivery.State == "applied" && p.Requests.Load() >= int64(i+1)
		})
		if view := p.Idle(t, test.binding.SessionID); view.LastRun.Status != "completed" {
			t.Fatal("reply did not complete in the bound session")
		}
		message, err := p.Store.Message(p.Context, test.binding.ConversationID, strconv.FormatInt(incoming.Message.ID, 10))
		if err != nil || message.Text != test.text {
			t.Fatalf("reply was not archived in its bound conversation: %v", err)
		}
		if i == 0 && (message.ReplyQuote == nil || message.ReplyQuote.Text != "selected <source>&" || message.ReplyQuote.Position != 3 || !message.ReplyQuote.Manual) {
			t.Fatal("selected quote did not survive the IMStore archive")
		}
	}
	conversations, err := list[domain.Conversation](p.Context, p.Store, "conversations")
	if err != nil || len(conversations) != 2 {
		t.Fatalf("ordinary replies created extra conversations: count=%d error=%v", len(conversations), err)
	}
	bindings, err := list[domain.ConversationBinding](p.Context, p.Store, "bindings")
	if err != nil || len(bindings) != 2 || p.Requests.Load() != 4 {
		t.Fatalf("reply routing created bindings or lost/duplicated model work: count=%d requests=%d error=%v", len(bindings), p.Requests.Load(), err)
	}
	// An actual, unbound Topic must remain outside the authorized group and
	// Topic sessions even when its message replies to this Bot.
	unbound := update(5, "unbound topic reply")
	unbound.Message.ThreadID, unbound.Message.IsTopicMessage = 88, true
	unbound.Message.Chat.IsForum = true
	unbound.Message.Reply = &telegramMessage{ID: 88, From: &telegramUser{ID: 999}, Text: "original"}
	bot.updates <- []telegramUpdate{unbound}
	testkit.Await(t, p.Context, func() bool {
		var offset cursor
		return p.Store.Get(p.Context, "channel_cursors", connection.ID, &offset) == nil && offset.Value == "6"
	})
	unboundID := stableID("conversation", connection.ID, "-100", "88")
	if _, err := p.Store.Message(p.Context, unboundID, "105"); err != nil {
		t.Fatal("real unbound Topic was not archived independently:", err)
	}
	bindings, err = list[domain.ConversationBinding](p.Context, p.Store, "bindings")
	if err != nil || len(bindings) != 2 || p.Requests.Load() != 4 {
		t.Fatal("unbound Topic gained access to an existing session")
	}
}

func TestTelegramQuotePreservesSelectedFragmentAndReplyPreview(t *testing.T) {
	selected := strings.Repeat("选", 1024)
	preview := strings.Repeat("原", 700) + selected
	var message telegramMessage
	raw := fmt.Sprintf(`{"message_id":43,"chat":{"id":-100,"type":"supergroup"},"text":"reply","quote":{"text":%q,"position":700,"is_manual":true},"reply_to_message":{"message_id":42,"text":%q,"from":{"id":999}}}`, selected, preview)
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatal(err)
	}
	adapter := &telegram{connection: domain.Connection{AccountID: "999"}}
	event := adapter.normalize(1, message, false).Observation
	if event.ReplyQuote == nil || event.ReplyQuote.Text != selected || event.ReplyQuote.Position != 700 || !event.ReplyQuote.Manual || event.ReplyPreview != strings.Repeat("原", 500) || event.Trigger != "reply" {
		t.Fatal("selected fragment was lost, truncated, or substituted for the original preview")
	}
	message.Quote = nil
	message.Reply.Text = ""
	if err := json.Unmarshal([]byte(`{"blocks":[{"type":"paragraph","text":"rich source"},{"type":"list","items":[{"label":"1.","blocks":[{"type":"paragraph","text":"item"}]}]}]}`), &message.Reply.RichMessage); err != nil {
		t.Fatal(err)
	}
	event = adapter.normalize(2, message, false).Observation
	if event.ReplyPreview != "rich source\n1. item" || event.ReplyQuote != nil {
		t.Fatal("rich reply did not retain its source preview independently of quotes")
	}
}

func TestTelegramExternalQuoteDoesNotInventLocalReplyOrTrigger(t *testing.T) {
	var message telegramMessage
	if err := json.Unmarshal([]byte(`{"message_id":43,"chat":{"id":-100,"type":"supergroup"},"text":"reply","external_reply":{"origin":{"type":"user","sender_user":{"id":999},"date":1792900000},"chat":{"id":-200,"type":"supergroup"},"message_id":42},"quote":{"text":"selected external fragment","position":0}}`), &message); err != nil {
		t.Fatal(err)
	}
	event := (&telegram{connection: domain.Connection{AccountID: "999"}}).normalize(1, message, false).Observation
	if event.ReplyID != "" || event.Trigger != "" || event.ReplyQuote == nil || event.ReplyQuote.Text != "selected external fragment" || event.ReplyQuote.Position != 0 || event.ReplyQuote.Manual {
		t.Fatal("external quote lost its content or gained an unauthorized local reply/trigger")
	}
}

func TestTelegramMentionInsideQuoteDoesNotTrigger(t *testing.T) {
	var message telegramMessage
	if err := json.Unmarshal([]byte(`{"message_id":43,"chat":{"id":-100,"type":"supergroup"},"text":"ordinary reply","reply_to_message":{"message_id":42,"from":{"id":7},"text":"@astral_bot"},"quote":{"text":"@astral_bot","position":0,"is_manual":true,"entities":[{"type":"bold","offset":0,"length":11}]}}`), &message); err != nil {
		t.Fatal(err)
	}
	adapter := &telegram{connection: domain.Connection{AccountID: "999", Username: "astral_bot"}}
	event := adapter.normalize(1, message, false).Observation
	if event.Trigger != "" || event.ReplyQuote == nil || event.ReplyQuote.Text != "@astral_bot" {
		t.Fatal("a mention in quoted source text triggered the current message")
	}
	message.Text = "@astral_bot ordinary reply"
	message.Entities = []telegramEntity{{Type: "mention", Offset: 0, Length: 11}}
	if adapter.normalize(2, message, false).Observation.Trigger != "mention" {
		t.Fatal("quoted source text suppressed an actual mention in the current message")
	}
}

func TestTelegramMessageIDOnlyReceiptRetainsTargetTopic(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	service := New(p.Store, p.Harness)
	connection := bot.connection(t, service, p.Agent.ID)
	binding := createBinding(t, p, service, connection, "-100", "77")
	var target domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &target); err != nil {
		t.Fatal(err)
	}
	adapter, err := service.adapter(p.Context, connection)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.archiveTelegramResult(p.Context, adapter.(*telegram), target, "copyMessage", map[string]any{}, json.RawMessage(`{"message_id":701}`)); err != nil {
		t.Fatal("message-ID-only receipt lost its already selected target:", err)
	}
	message, err := p.Store.Message(p.Context, target.ID, "701")
	if err != nil || !message.Self || message.ConversationID != target.ID || message.Trigger != "" {
		t.Fatalf("receipt did not remain in the authorized Topic: %v", err)
	}
}

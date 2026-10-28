package channels

import (
	"context"
	"fmt"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestTelegramChatIDCommandRecognition(t *testing.T) {
	adapter := &telegram{connection: domain.Connection{Meta: domain.Meta{ID: "account"}, AccountID: "999", Username: "astral_bot"}}
	for _, test := range []struct {
		name, text, caption          string
		edited, bot, self, anonymous bool
		command, reply               bool
	}{
		{name: "plain", text: "/chat_id", command: true, reply: true},
		{name: "whitespace", text: " \n/chat_id\t", command: true, reply: true},
		{name: "addressed", text: "/chat_id@astral_bot", command: true, reply: true},
		{name: "username case", text: "/chat_id@ASTRAL_BOT", command: true, reply: true},
		{name: "other bot", text: "/chat_id@other_bot"},
		{name: "missing username", text: "/chat_id@"},
		{name: "hyphen", text: "/chat-id"},
		{name: "argument", text: "/chat_id please"},
		{name: "embedded", text: "try /chat_id"},
		{name: "multiline", text: "/chat_id\nmore"},
		{name: "caption", caption: "/chat_id"},
		{name: "edited", text: "/chat_id", edited: true, command: true},
		{name: "another bot", text: "/chat_id", bot: true, command: true},
		{name: "self", text: "/chat_id", self: true, command: true},
		{name: "anonymous admin", text: "/chat_id", bot: true, anonymous: true, command: true, reply: true},
		{name: "self with sender chat", text: "/chat_id", self: true, anonymous: true, command: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := *update(1, test.text).Message
			message.Caption, message.From.IsBot = test.caption, test.bot
			if test.self {
				message.From.ID = 999
			}
			if test.anonymous {
				message.SenderChat = &message.Chat
			}
			event := adapter.normalize(1, message, test.edited)
			if (event.Command != nil) != test.command {
				t.Fatalf("command = %+v, want recognized = %v", event.Command, test.command)
			}
			if event.Command != nil && (event.Command.Text != "") != test.reply {
				t.Fatalf("reply = %q, want reply = %v", event.Command.Text, test.reply)
			}
		})
	}
}

func TestChatIDCommandsReplyBeforeAuthorizationWithoutActivatingSessions(t *testing.T) {
	for _, scope := range []string{"specified", "all"} {
		t.Run(scope, func(t *testing.T) {
			bot := newBot(t)
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
				t.Error("Chat ID query called a model")
				return testkit.Text("unexpected")
			})
			p.Agent.DefaultModelID = ""
			if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
				t.Fatal(err)
			}
			service := New(p.Store, p.Harness)
			connection := bot.connection(t, service, p.Agent.ID)
			connection.AccessMode = scope
			if err := service.SaveConnection(p.Context, &connection, nil); err != nil {
				t.Fatal(err)
			}
			private, group, topic, channel := update(1, "/chat_id"), update(2, "/chat_id@astral_bot"), update(3, "/chat_id"), update(4, "/chat_id")
			private.Message.Chat = telegramChat{ID: 42, Type: "private", FirstName: "Ada"}
			topic.Message.ThreadID = 77
			topic.Message.IsTopicMessage = true
			topic.Message.Chat.IsForum = true
			channel.Message.Chat = telegramChat{ID: -10099, Type: "channel", Title: "News"}
			channel.Message.From = nil
			channel.ChannelPost, channel.Message = channel.Message, nil
			bot.updates <- []telegramUpdate{private, group, topic, channel, group}
			startChannels(t, service, p.Context)
			testkit.Await(t, p.Context, func() bool {
				var offset cursor
				return p.Store.Get(p.Context, "channel_cursors", connection.ID, &offset) == nil && offset.Value == "5"
			})
			bot.mu.Lock()
			sent := append([]map[string]any(nil), bot.sendParameters...)
			bot.mu.Unlock()
			if len(sent) != 4 || bot.sends.Load() != 4 {
				t.Fatalf("received %d replies / %d attempts; want 4 including duplicate suppression", len(sent), bot.sends.Load())
			}
			for i, want := range []struct{ chat, text string }{
				{"42", "Chat ID: 42"}, {"-100", "Chat ID: -100"},
				{"-100", "Chat ID: -100\nTopic ID: 77"}, {"-10099", "Chat ID: -10099"},
			} {
				body := sent[i]
				if body["chat_id"] != want.chat || body["text"] != want.text {
					t.Errorf("reply %d = %+v", i, body)
				}
				reply, _ := body["reply_parameters"].(map[string]any)
				if reply["message_id"] != float64(101+i) {
					t.Errorf("reply %d lost the original message: %+v", i, body)
				}
				if i == 2 && body["message_thread_id"] != float64(77) || i != 2 && body["message_thread_id"] != nil {
					t.Errorf("reply %d was routed to the wrong Topic: %+v", i, body)
				}
			}
			for kind, want := range map[string]int{"sessions": 1, "bindings": 0, "outbox": 0, "channel_commands": 4} {
				records, err := p.Store.List(p.Context, kind)
				if err != nil || len(records) != want {
					t.Errorf("%s: got %d records, want %d, error %v", kind, len(records), want, err)
				}
			}
			conversations, err := list[domain.Conversation](p.Context, p.Store, "conversations")
			if err != nil || len(conversations) != 4 {
				t.Fatalf("discovered conversations = %d, error %v", len(conversations), err)
			}
			for _, conversation := range conversations {
				observations, err := p.Store.Observations(p.Context, conversation.ID, 0, 100)
				if err != nil || len(observations) != 0 {
					t.Errorf("utility request/reply entered model observations: %v, %v", observations, err)
				}
			}
			if p.Requests.Load() != 0 {
				t.Fatal("Chat ID query called a model")
			}
		})
	}
}

func TestChatIDCommandRecoveryNeverRepeatsAnAttempt(t *testing.T) {
	for _, outcome := range []string{"accepted", "unknown", "dispatching"} {
		t.Run(outcome, func(t *testing.T) {
			bot := newBot(t)
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return nil })
			service := New(p.Store, p.Harness)
			connection := bot.connection(t, service, p.Agent.ID)
			adapter, err := service.adapter(p.Context, connection)
			if err != nil {
				t.Fatal(err)
			}
			event := adapter.(*telegram).normalize(1, *update(1, "/chat_id").Message, false)
			bot.unknown.Store(outcome == "unknown")
			err = service.replyToCommand(p.Context, adapter, event)
			if (err != nil) != (outcome == "unknown") {
				t.Fatalf("send error = %v for outcome %s", err, outcome)
			}
			records, err := list[commandReceipt](p.Context, p.Store, "channel_commands")
			if err != nil || len(records) != 1 {
				t.Fatalf("receipts = %+v, error %v", records, err)
			}
			if outcome == "dispatching" {
				// Simulate a server stopping after Telegram accepted the request,
				// but before the acceptance receipt was durably recorded.
				records[0].Status, records[0].PlatformMessageIDs = "dispatching", nil
				if err := p.Store.Save(p.Context, "channel_commands", &records[0].Meta, &records[0]); err != nil {
					t.Fatal(err)
				}
			}
			bot.unknown.Store(false)
			restarted := New(p.Store, p.Harness)
			if err := restarted.recoverCommands(p.Context); err != nil {
				t.Fatal(err)
			}
			// Even a replay with a different update ID has the same message identity.
			event.Observation.ID = fmt.Sprint("replayed-", outcome)
			if err := restarted.replyToCommand(p.Context, adapter, event); err != nil {
				t.Fatal(err)
			}
			if bot.sends.Load() != 1 {
				t.Fatal("recovery sent a second reply")
			}
			var record commandReceipt
			if err := p.Store.Get(p.Context, "channel_commands", records[0].ID, &record); err != nil {
				t.Fatal(err)
			}
			want := outcome
			if want == "dispatching" {
				want = "unknown"
			}
			if record.Status != want {
				t.Fatalf("receipt state = %q, want %q", record.Status, want)
			}
		})
	}
}

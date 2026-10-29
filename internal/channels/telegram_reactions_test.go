package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestTelegramReactionChangesPreserveActorsAndOnlyReportDifferences(t *testing.T) {
	adapter := &telegram{connection: domain.Connection{Meta: domain.Meta{ID: "account"}, AccountID: "999"}}
	thumb := telegramReactionType{Type: "emoji", Emoji: "👍"}
	heart := telegramReactionType{Type: "emoji", Emoji: "❤"}
	custom := telegramReactionType{Type: "custom_emoji", CustomEmojiID: "5368324170671202286"}
	paid := telegramReactionType{Type: "paid"}
	for _, test := range []struct {
		name      string
		before    []telegramReactionType
		after     []telegramReactionType
		user      *telegramUser
		actor     *telegramChat
		actorType string
		sender    string
		kinds     []string
		reactions [][]telegramReactionType
	}{
		{name: "add", after: []telegramReactionType{thumb}, user: &telegramUser{ID: 7, FirstName: "Ada", Username: "ada"}, actorType: "user", sender: "7", kinds: []string{"added"}, reactions: [][]telegramReactionType{{thumb}}},
		{name: "remove", before: []telegramReactionType{thumb}, user: &telegramUser{ID: 7}, actorType: "user", sender: "7", kinds: []string{"removed"}, reactions: [][]telegramReactionType{{thumb}}},
		{name: "replace retaining common", before: []telegramReactionType{thumb, heart}, after: []telegramReactionType{heart, custom}, user: &telegramUser{ID: 7}, actorType: "user", sender: "7", kinds: []string{"removed", "added"}, reactions: [][]telegramReactionType{{thumb}, {custom}}},
		{name: "anonymous representative", after: []telegramReactionType{custom}, actor: &telegramChat{ID: -200, Title: "Publisher", Username: "publisher"}, actorType: "chat", sender: "chat:-200", kinds: []string{"added"}, reactions: [][]telegramReactionType{{custom}}},
		{name: "unknown actor", after: []telegramReactionType{paid}, actorType: "unknown", kinds: []string{"added"}, reactions: [][]telegramReactionType{{paid}}},
		{name: "identical", before: []telegramReactionType{thumb}, after: []telegramReactionType{thumb}, actorType: "unknown"},
		{name: "empty", actorType: "unknown"},
		{name: "duplicate member", after: []telegramReactionType{thumb, thumb}, actorType: "unknown", kinds: []string{"added"}, reactions: [][]telegramReactionType{{thumb}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			update := telegramMessageReaction{Chat: telegramChat{ID: -100, Type: "supergroup", Title: "Group", IsForum: true}, MessageID: 101, Date: 1793246400, User: test.user, ActorChat: test.actor, OldReaction: test.before, NewReaction: test.after}
			events := adapter.normalizeReaction(50, update)
			replay := adapter.normalizeReaction(50, update)
			if len(events) != len(test.kinds) {
				t.Fatalf("unexpected changes: %+v", events)
			}
			seen := map[string]bool{}
			for i, event := range events {
				observation := event.Observation
				if observation.Kind != "message.reaction."+test.kinds[i] || observation.Reaction == nil || observation.Reaction.ActorType != test.actorType || observation.Sender.ID != test.sender {
					t.Fatalf("actor or change semantics lost: %+v", observation)
				}
				if seen[observation.ID] || observation.ID != replay[i].Observation.ID || observation.ExternalEventID != replay[i].Observation.ExternalEventID {
					t.Fatalf("change IDs collide or polling replay is not stable: %+v", observation)
				}
				seen[observation.ID] = true
				if observation.MessageID != "101" || observation.Trigger != "" || observation.Self || !observation.SentAt.Equal(time.Unix(update.Date, 0)) || event.Conversation.TopicID != "" {
					t.Fatalf("invented trigger, time or Topic: %+v", event)
				}
				if len(observation.Reaction.Reactions) != len(test.reactions[i]) {
					t.Fatalf("wrong reaction difference: %+v", observation.Reaction)
				}
				for j, actual := range observation.Reaction.Reactions {
					want := test.reactions[i][j].reaction()
					if actual.Type != want.Type || actual.Emoji != want.Emoji || actual.ID != want.ID || actual.Count != nil || actual.ReportedCount != nil {
						t.Fatalf("individual change acquired totals or lost its identifier: %+v", actual)
					}
				}
			}
		})
	}
	missingDate := adapter.normalizeReaction(51, telegramMessageReaction{Chat: telegramChat{ID: -100}, MessageID: 101, NewReaction: []telegramReactionType{thumb}})
	if !missingDate[0].Observation.SentAt.IsZero() {
		t.Fatal("missing platform time was replaced by an invented timestamp")
	}
}

func TestTelegramReactionCountUpdatesRemainAnonymousAbsoluteSnapshots(t *testing.T) {
	adapter := &telegram{connection: domain.Connection{Meta: domain.Meta{ID: "account"}}}
	update := telegramMessageReactionCount{Chat: telegramChat{ID: -100, Type: "channel"}, MessageID: 101, Date: 1793246400, Reactions: []telegramReactionCount{
		{Type: telegramReactionType{Type: "emoji", Emoji: "👍"}, TotalCount: 12},
		{Type: telegramReactionType{Type: "custom_emoji", CustomEmojiID: "custom-id"}, TotalCount: 3},
		{Type: telegramReactionType{Type: "paid"}, TotalCount: 24},
	}}
	event := adapter.normalizeReactionCount(52, update).Observation
	if event.Kind != "message.reaction.counts_updated" || event.Sender != (domain.Sender{}) || event.Trigger != "" || event.Reaction == nil || event.Reaction.ActorType != "unknown" {
		t.Fatalf("anonymous totals acquired a sender or direct trigger: %+v", event)
	}
	for i, reaction := range event.Reaction.Reactions {
		if reaction.Count == nil || *reaction.Count != update.Reactions[i].TotalCount || reaction.ReportedCount != nil {
			t.Fatalf("count was treated as a delta: %+v", reaction)
		}
	}
	cleared := adapter.normalizeReactionCount(53, telegramMessageReactionCount{Chat: update.Chat, MessageID: 101, Date: update.Date}).Observation
	if cleared.Reaction == nil || len(cleared.Reaction.Reactions) != 0 {
		t.Fatal("empty absolute snapshot was dropped")
	}
}

func TestTelegramPollSubscribesToReactionsAndKeepsUpdateCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottest-token/getUpdates" {
			t.Errorf("unexpected API call %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var parameters struct {
			Offset         int64    `json:"offset"`
			AllowedUpdates []string `json:"allowed_updates"`
		}
		if err := json.NewDecoder(r.Body).Decode(&parameters); err != nil {
			t.Error(err)
			return
		}
		for _, kind := range []string{"message", "edited_message", "channel_post", "edited_channel_post", "message_reaction", "message_reaction_count"} {
			if !slices.Contains(parameters.AllowedUpdates, kind) {
				t.Errorf("missing subscription %s", kind)
			}
		}
		if parameters.Offset != 50 {
			t.Errorf("polling cursor changed: %d", parameters.Offset)
		}
		_, _ = fmt.Fprint(w, `{"ok":true,"result":[{"update_id":50,"message_reaction":{"chat":{"id":-100,"type":"supergroup"},"message_id":101,"user":{"id":7,"first_name":"Ada"},"date":1793246400,"old_reaction":[{"type":"emoji","emoji":"👍"}],"new_reaction":[{"type":"custom_emoji","custom_emoji_id":"custom-id"}]}},{"update_id":51,"message_reaction_count":{"chat":{"id":-200,"type":"channel"},"message_id":102,"date":1793246401,"reactions":[{"type":{"type":"emoji","emoji":"👍"},"total_count":12}]}},{"update_id":52,"message_reaction":{"chat":{"id":-100,"type":"supergroup"},"message_id":101,"date":1793246402,"old_reaction":[],"new_reaction":[]}}]}`)
	}))
	t.Cleanup(server.Close)
	adapter, err := NewTelegram(domain.Connection{Meta: domain.Meta{ID: "account"}, Config: encoded(TelegramConfig{BaseURL: server.URL})}, "test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := adapter.Poll(context.Background(), "50")
	if err != nil || batch.NextCursor != "53" || len(batch.Events) != 3 {
		t.Fatalf("reaction polling lost a difference or cursor: %+v %v", batch, err)
	}
	for i, kind := range []string{"message.reaction.removed", "message.reaction.added", "message.reaction.counts_updated"} {
		if batch.Events[i].Observation.Kind != kind {
			t.Fatalf("unexpected reaction order: %+v", batch.Events)
		}
	}
}

func TestTelegramReactionOperationsArchiveOnlyConfirmedSelections(t *testing.T) {
	var mode atomic.Int64
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(result any) { _ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}) }
		switch strings.TrimPrefix(r.URL.Path, "/bottest-token/") {
		case "getMe":
			reply(telegramUser{ID: 999, IsBot: true, Username: "astral_bot"})
		case "getWebhookInfo":
			reply(map[string]any{"url": ""})
		case "getChat":
			var parameters map[string]any
			_ = json.NewDecoder(r.Body).Decode(&parameters)
			id, _ := strconv.ParseInt(telegramID(parameters["chat_id"]), 10, 64)
			reply(telegramChat{ID: id, Type: "supergroup", Title: "Reactions", IsForum: true})
		case "setMessageReaction":
			calls.Add(1)
			var parameters map[string]any
			if err := json.NewDecoder(r.Body).Decode(&parameters); err != nil {
				t.Error(err)
				return
			}
			if parameters["chat_id"] != "-100" || telegramID(parameters["message_id"]) != "101" || parameters["message_thread_id"] != nil {
				t.Errorf("operation escaped its archived Topic: %v", parameters)
			}
			switch mode.Load() {
			case 1:
				_, _ = fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"REACTION_INVALID"}`)
			case 2:
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
			case 3:
				reply(false)
			default:
				reply(true)
			}
		default:
			t.Errorf("unexpected API call %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	service := New(p.Store, p.Harness)
	connection := domain.Connection{Name: "reaction test", AgentID: p.Agent.ID, Adapter: "telegram.bot", Enabled: true, Config: encoded(TelegramConfig{BaseURL: server.URL})}
	token := "test-token"
	if err := service.SaveConnection(p.Context, &connection, &token); err != nil {
		t.Fatal(err)
	}
	topic := createBinding(t, p, service, connection, "-100", "22")
	execution := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: topic.SessionID}
	request := func(messageID string, reactions any) error {
		_, err := service.AccountOperation(p.Context, execution, connection.ID, "setMessageReaction", ChatOperationRequest{ConversationID: topic.ConversationID, Parameters: map[string]any{"message_id": messageID, "reaction": reactions}})
		return err
	}
	selected := []any{map[string]any{"type": "emoji", "emoji": "👍"}}
	if err := request("101", selected); err == nil || calls.Load() != 0 {
		t.Fatal("unknown Topic message reached Telegram")
	}
	original := domain.Observation{SchemaVersion: 1, ID: "original", ExternalEventID: "original", ConnectionID: connection.ID, ConversationID: topic.ConversationID, Kind: "message.created", MessageID: "101", Text: "Original", SentAt: time.Now(), ReceivedAt: time.Now()}
	if _, err := p.Store.Archive(p.Context, original); err != nil {
		t.Fatal(err)
	}
	for _, reactions := range []any{selected, []any{}, selected, string(encoded(selected))} {
		if err := request("101", reactions); err != nil {
			t.Fatal(err)
		}
	}
	events, err := p.Store.Observations(p.Context, topic.ConversationID, 0, 100)
	if err != nil || len(events) != 5 {
		t.Fatalf("successive set/clear/set facts were lost: %+v %v", events, err)
	}
	seen := map[string]bool{}
	for i, stored := range events[1:] {
		event := stored.Observation
		if event.Kind != "message.reaction.set" || !event.Self || event.Trigger != "" || event.Sender.ID != "999" || event.Reaction == nil || event.Reaction.ActorType != "user" || event.MessageID != "101" || !strings.Contains(event.Text, "requested message 101") || !strings.Contains(event.Text, "media group") {
			t.Fatalf("accepted selection lost operation semantics: %+v", event)
		}
		if seen[event.ID] {
			t.Fatal("set/clear/set was incorrectly deduplicated")
		}
		seen[event.ID] = true
		want := 1
		if i == 1 {
			want = 0
		}
		if len(event.Reaction.Reactions) != want {
			t.Fatalf("clear or set selection was lost: %+v", event.Reaction)
		}
	}
	for _, failure := range []int64{1, 2, 3} {
		mode.Store(failure)
		err := request("101", selected)
		if failure != 3 && err == nil {
			t.Fatalf("upstream failure %d appeared successful", failure)
		}
	}
	after, err := p.Store.Observations(p.Context, topic.ConversationID, 0, 100)
	if err != nil || len(after) != len(events) {
		t.Fatalf("known, unknown or false results created success facts: %+v %v", after, err)
	}
}

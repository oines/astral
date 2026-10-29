package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func feishuReactionTestEvent(eventID, messageID, actorType, actorID string, removed bool) []byte {
	eventType := "im.message.reaction.created_v1"
	if removed {
		eventType = "im.message.reaction.deleted_v1"
	}
	event := map[string]any{
		"message_id": messageID, "reaction_type": map[string]string{"emoji_type": "THUMBSUP"},
		"operator_type": actorType, "action_time": "1700000000123",
	}
	if actorType == "user" {
		event["user_id"] = map[string]string{"open_id": actorID}
	} else {
		event["app_id"] = actorID
	}
	return encoded(map[string]any{"schema": "2.0", "header": map[string]string{
		"app_id": "cli_app", "event_type": eventType, "event_id": eventID,
	}, "event": event})
}

func TestFeishuReactionNormalizePreservesActorAndStableIdentity(t *testing.T) {
	f := newFeishuFixture(t)
	for _, scenario := range []struct {
		name, actorType, actorID string
		self                     bool
	}{
		{"human", "user", "ou_user", false},
		{"other_app", "app", "cli_other", false},
		{"own_app", "app", "cli_app", true},
		{"bot_open_id_is_not_app_id", "app", "ou_bot", false},
		{"actor_name_unavailable", "user", "", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var in larkim.P2MessageReactionCreatedV1
			if err := json.Unmarshal(feishuReactionTestEvent("evt_"+scenario.name, "om_incoming", scenario.actorType, scenario.actorID, false), &in); err != nil {
				t.Fatal(err)
			}
			event, err := f.adapter.normalizeReaction(context.Background(), in.EventV2Base, in.Event, false)
			if err != nil || event == nil {
				t.Fatalf("normalize: %#v, %v", event, err)
			}
			o := event.Observation
			if event.Conversation.ExternalID != "oc_chat" || o.ConversationID != event.Conversation.ID || o.MessageID != "om_incoming" ||
				o.Kind != "message.reaction.added" || o.Trigger != "" || o.Self != scenario.self || o.Sender.ID != scenario.actorID ||
				o.Sender.Name != "" || o.SentAt.UnixMilli() != 1700000000123 || o.Reaction == nil || o.Reaction.ActorType != scenario.actorType ||
				len(o.Reaction.Reactions) != 1 || o.Reaction.Reactions[0].Type != "feishu" || o.Reaction.Reactions[0].ID != "THUMBSUP" {
				t.Fatalf("reaction information changed: %#v", event)
			}
			redelivered, err := f.adapter.normalizeReaction(context.Background(), in.EventV2Base, in.Event, false)
			if err != nil || redelivered.Observation.ID != o.ID || redelivered.Observation.ExternalEventID != o.ExternalEventID {
				t.Fatalf("redelivery changed event identity: %#v, %v", redelivered, err)
			}
			removed, err := f.adapter.normalizeReaction(context.Background(), in.EventV2Base, in.Event, true)
			if err != nil || removed.Observation.Kind != "message.reaction.removed" {
				t.Fatalf("removal lost its event kind: %#v, %v", removed, err)
			}
		})
	}
}

func TestFeishuReactionRejectsMalformedAndForeignEvents(t *testing.T) {
	f := newFeishuFixture(t)
	for _, mutate := range []func(*larkim.P2MessageReactionCreatedV1){
		func(in *larkim.P2MessageReactionCreatedV1) { in.EventV2Base = nil },
		func(in *larkim.P2MessageReactionCreatedV1) { in.EventV2Base.Header = nil },
		func(in *larkim.P2MessageReactionCreatedV1) { in.EventV2Base.Header.EventID = "" },
		func(in *larkim.P2MessageReactionCreatedV1) { in.EventV2Base.Header.AppID = "cli_other" },
		func(in *larkim.P2MessageReactionCreatedV1) { in.Event = nil },
		func(in *larkim.P2MessageReactionCreatedV1) { in.Event.ReactionType = nil },
		func(in *larkim.P2MessageReactionCreatedV1) { in.Event.MessageId = nil },
		func(in *larkim.P2MessageReactionCreatedV1) { in.Event.ActionTime = nil },
		func(in *larkim.P2MessageReactionCreatedV1) { v := "bad"; in.Event.ActionTime = &v },
		func(in *larkim.P2MessageReactionCreatedV1) { v := "unknown"; in.Event.OperatorType = &v },
		func(in *larkim.P2MessageReactionCreatedV1) { v := "invalid/id"; in.Event.UserId.OpenId = &v },
	} {
		var in larkim.P2MessageReactionCreatedV1
		if err := json.Unmarshal(feishuReactionTestEvent("evt_invalid", "om_incoming", "user", "ou_user", false), &in); err != nil {
			t.Fatal(err)
		}
		mutate(&in)
		if event, err := f.adapter.normalizeReaction(context.Background(), in.EventV2Base, in.Event, false); err == nil || event != nil {
			t.Fatalf("invalid event accepted: %#v, %v", event, err)
		}
	}
}

func TestFeishuReactionWebsocketACKWaitsForDurableCursor(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprint(removed), func(t *testing.T) {
			f := newFeishuFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			f.events <- feishuReactionTestEvent("evt_reaction", "om_incoming", "user", "ou_user", removed)
			batch, err := f.adapter.Poll(ctx, "")
			kind := "message.reaction.added"
			if removed {
				kind = "message.reaction.removed"
			}
			if err != nil || len(batch.Events) != 1 || batch.NextCursor != "evt_reaction" || batch.Events[0].Observation.Kind != kind || batch.Events[0].Observation.Reaction == nil {
				t.Fatalf("reaction callback: %#v, %v", batch, err)
			}
			select {
			case ack := <-f.acks:
				t.Fatalf("reaction ACK preceded durable persistence: %#v", ack)
			case <-time.After(40 * time.Millisecond):
			}
			replayed, err := f.adapter.Poll(ctx, "")
			if err != nil || len(replayed.Events) != 1 || replayed.Events[0].Observation.ID != batch.Events[0].Observation.ID {
				t.Fatalf("uncommitted reaction not replayed: %#v, %v", replayed, err)
			}
			f.events <- feishuTestEvent("evt_after", "group", "text", `{"text":"next"}`)
			if _, err := f.adapter.Poll(ctx, batch.NextCursor); err != nil {
				t.Fatal(err)
			}
			select {
			case ack := <-f.acks:
				var response struct{ Code int }
				if json.Unmarshal(ack.Payload, &response) != nil || response.Code != http.StatusOK {
					t.Fatalf("reaction success ACK: %s", ack.Payload)
				}
			case <-ctx.Done():
				t.Fatal("no reaction ACK after durable cursor advanced")
			}
		})
	}
}

func TestFeishuReactionUnavailableSourceDoesNotHideOtherMessages(t *testing.T) {
	for _, code := range []int{230110, 231003, 231008} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := newFeishuFixture(t)
			original := f.server.Config.Handler
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/open-apis/im/v1/messages/om_unknown" {
					_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "message unavailable"})
					return
				}
				original.ServeHTTP(w, r)
			})
			var in larkim.P2MessageReactionCreatedV1
			_ = json.Unmarshal(feishuReactionTestEvent("evt_unknown", "om_unknown", "user", "ou_user", false), &in)
			event, err := f.adapter.normalizeReaction(context.Background(), in.EventV2Base, in.Event, false)
			if event != nil || (code == 231008) != (err != nil) {
				t.Fatalf("permanent missing source and permissions were conflated: %#v, %v", event, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			f.events <- feishuReactionTestEvent("evt_unknown", "om_unknown", "user", "ou_user", false)
			f.events <- feishuTestEvent("evt_next", "group", "text", `{"text":"still delivered"}`)
			batch, err := f.adapter.Poll(ctx, "")
			if err != nil || len(batch.Events) != 1 || batch.Events[0].Observation.Text != "still delivered" {
				t.Fatalf("unroutable reaction blocked normal messages: %#v, %v", batch, err)
			}
		})
	}
}

func TestFeishuReactionUsesArchivedDeletedMessageWithoutRemoteLookup(t *testing.T) {
	p := testkit.New(t, 1, nil)
	f := newFeishuFixture(t)
	f.adapter.setStore(p.Store)
	chat := domain.Conversation{ConnectionID: f.adapter.connection.ID, ExternalID: "oc_archived", Kind: "group", Title: "Archived chat"}
	chat.ID = stableID("conversation", chat.ConnectionID, chat.ExternalID, "")
	if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{"message.created", "message.deleted"} {
		o := domain.Observation{SchemaVersion: 1, ID: fmt.Sprintf("history_%d", i), ConnectionID: chat.ConnectionID,
			ConversationID: chat.ID, ExternalEventID: fmt.Sprintf("history_%d", i), MessageID: "om_archived", Kind: kind}
		if _, err := p.Store.Archive(p.Context, o); err != nil {
			t.Fatal(err)
		}
	}
	original := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/open-apis/im/v1/") {
			t.Error("archived source caused an unnecessary Feishu API lookup")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		original.ServeHTTP(w, r)
	})
	var in larkim.P2MessageReactionCreatedV1
	_ = json.Unmarshal(feishuReactionTestEvent("evt_archived", "om_archived", "user", "ou_user", false), &in)
	event, err := f.adapter.normalizeReaction(p.Context, in.EventV2Base, in.Event, false)
	if err != nil || event == nil || event.Conversation.ID != chat.ID || event.Conversation.Title != chat.Title {
		t.Fatalf("archived message lost its original chat: %#v, %v", event, err)
	}
}

func TestFeishuServiceArchivesReactionEventsWithoutRewritingMessage(t *testing.T) {
	p := testkit.New(t, 1, nil)
	f := newFeishuFixture(t)
	service := New(p.Store, p.Harness)
	connection := domain.Connection{Name: "Feishu reaction integration", AgentID: p.Agent.ID, Adapter: "feishu.bot", Enabled: true,
		AccessMode: "all", Config: encoded(FeishuConfig{AppID: "cli_app", BaseURL: f.server.URL})}
	secret := "app-secret"
	if err := service.SaveConnection(p.Context, &connection, &secret); err != nil {
		t.Fatal(err)
	}
	chat, err := service.ResolveForAgent(p.Context, p.Agent.ID, connection.ID, "oc_chat", "")
	if err != nil {
		t.Fatal(err)
	}
	startChannels(t, service, p.Context)
	f.events <- feishuServiceEvent("evt_original", "om_original", "group", "original text", false, false)
	testkit.Await(t, p.Context, func() bool { _, err := p.Store.Message(p.Context, chat.ID, "om_original"); return err == nil })
	f.events <- feishuReactionTestEvent("evt_add", "om_original", "user", "ou_user", false)
	testkit.Await(t, p.Context, func() bool { n, _ := p.Store.ObservationHead(p.Context, chat.ID); return n >= 2 })
	f.events <- feishuReactionTestEvent("evt_remove", "om_original", "user", "ou_user", true)
	testkit.Await(t, p.Context, func() bool { n, _ := p.Store.ObservationHead(p.Context, chat.ID); return n >= 3 })
	// Replayed event IDs must converge on the same two immutable observations.
	f.events <- feishuReactionTestEvent("evt_add", "om_original", "user", "ou_user", false)
	f.events <- feishuReactionTestEvent("evt_remove", "om_original", "user", "ou_user", true)
	f.events <- feishuServiceEvent("evt_after", "om_after", "group", "after reactions", false, false)
	testkit.Await(t, p.Context, func() bool { _, err := p.Store.Message(p.Context, chat.ID, "om_after"); return err == nil })
	records, err := p.Store.Observations(p.Context, chat.ID, 0, 100)
	if err != nil || len(records) != 4 || records[0].Observation.Text != "original text" ||
		records[1].Observation.Kind != "message.reaction.added" || records[2].Observation.Kind != "message.reaction.removed" {
		t.Fatalf("reactions overwrote or duplicated chat observations: %#v, %v", records, err)
	}
	message, err := p.Store.Message(p.Context, chat.ID, "om_original")
	if err != nil || message.Text != "original text" || message.Kind != "message.created" || message.Reaction != nil {
		t.Fatalf("message lookup returned a reaction instead of its source: %#v, %v", message, err)
	}
	if p.Requests.Load() != 0 {
		t.Fatalf("reaction events directly activated the model: %d requests", p.Requests.Load())
	}
}

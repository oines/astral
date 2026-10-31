package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
)

func TestNapCatReactionsAreAppendOnlyNotices(t *testing.T) {
	n := &napcat{connection: domain.Connection{Meta: domain.Meta{ID: "n"}, AccountID: "999"}}
	for _, test := range []struct {
		name, user, kind, actorType string
		add, self                   bool
	}{
		{"added", `123`, "message.reaction.added", "user", true, false},
		{"removed", `123`, "message.reaction.removed", "user", false, false},
		{"self", `999`, "message.reaction.added", "user", true, true},
		{"unknown zero", `0`, "message.reaction.added", "unknown", true, false},
		{"unknown null", `null`, "message.reaction.added", "unknown", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"post_type":"notice","notice_type":"group_msg_emoji_like","self_id":999,"group_id":456,"group_name":"Team","user_id":%s,"message_id":-42,"time":1780000000,"is_add":%t,"likes":[{"emoji_id":"76","count":1},{"emoji_id":128077,"count":0}]}`, test.user, test.add)
			event, err := n.normalize(json.RawMessage(raw))
			if err != nil || event == nil {
				t.Fatalf("normalize reaction: %+v %v", event, err)
			}
			observation := event.Observation
			if event.Conversation.ExternalID != "group:456" || event.Conversation.Title != "Team" || observation.Kind != test.kind || observation.MessageID != "-42" || observation.Self != test.self || observation.Trigger != "" {
				t.Fatalf("reaction scope or scheduling changed: %+v", event)
			}
			if observation.Reaction == nil || observation.Reaction.ActorType != test.actorType || len(observation.Reaction.Reactions) != 2 {
				t.Fatalf("missing reaction details: %+v", observation)
			}
			first, second := observation.Reaction.Reactions[0], observation.Reaction.Reactions[1]
			if first.Type != "qq" || first.ID != "76" || first.Emoji != "" || first.Count != nil || first.ReportedCount == nil || *first.ReportedCount != 1 || second.ID != "128077" || second.Count != nil || second.ReportedCount == nil || *second.ReportedCount != 0 {
				t.Fatalf("reported count or emoji identity was invented: %+v", observation.Reaction)
			}
			if test.actorType == "unknown" && (observation.Sender.ID != "" || observation.Sender.Name != "" || !strings.Contains(observation.Text, "unknown QQ user")) {
				t.Fatalf("unknown actor was fabricated: %+v", observation)
			}
			if observation.SentAt.Unix() != 1780000000 || observation.ID == "" || observation.ExternalEventID == "" {
				t.Fatalf("missing event identity/time: %+v", observation)
			}
			again, err := n.normalize(json.RawMessage(raw))
			if err != nil || again.Observation.ID == observation.ID {
				t.Fatal("separate add/remove notices without a platform event ID were collapsed")
			}
		})
	}
}

func TestNapCatReactionValidationAndPendingReplay(t *testing.T) {
	n := &napcat{connection: domain.Connection{Meta: domain.Meta{ID: "n"}, AccountID: "999"}}
	for _, invalid := range []string{
		`"self_id":888,"group_id":456,"message_id":-42,"is_add":true,"likes":[{"emoji_id":"76","count":1}]`,
		`"self_id":999,"group_id":0,"message_id":-42,"is_add":true,"likes":[{"emoji_id":"76","count":1}]`,
		`"self_id":999,"group_id":456,"message_id":-42,"likes":[{"emoji_id":"76","count":1}]`,
		`"self_id":999,"group_id":456,"message_id":-42,"is_add":true,"likes":[]`,
		`"self_id":999,"group_id":456,"message_id":-42,"is_add":true,"likes":[{"emoji_id":"76","count":-1}]`,
		`"self_id":999,"group_id":456,"message_id":-42,"is_add":true,"likes":[{"emoji_id":"","count":1}]`,
	} {
		if _, err := n.normalize(json.RawMessage(`{"post_type":"notice","notice_type":"group_msg_emoji_like",` + invalid + `}`)); err == nil {
			t.Fatalf("accepted malformed reaction: %s", invalid)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer socket.CloseNow()
		_ = wsjson.Write(ctx, socket, json.RawMessage(`{"post_type":"notice","notice_type":"group_msg_emoji_like","self_id":999,"group_id":456,"user_id":123,"message_id":-42,"is_add":true,"likes":[{"emoji_id":"76","count":1}]}`))
		_, _, _ = socket.Read(ctx)
	}))
	defer server.Close()
	n = napcatTestAdapter(t, server)
	batch, err := n.Poll(ctx, "")
	if err != nil || len(batch.Events) != 1 || batch.Events[0].Observation.Kind != "message.reaction.added" {
		t.Fatalf("poll reaction: %+v %v", batch, err)
	}
	replay, err := n.Poll(ctx, "")
	if err != nil || len(replay.Events) != 1 || replay.Events[0].Observation.ID != batch.Events[0].Observation.ID {
		t.Fatalf("pending replay replaced reaction identity: %+v %v", replay, err)
	}
	n.socket.CloseNow()
}

func TestNapCatSetReactionForwardsOnlyValidatedAction(t *testing.T) {
	for _, test := range []struct {
		name string
		set  any
		want bool
	}{
		{"default add", nil, true}, {"add", true, true}, {"remove", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/get_msg" {
					fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"message_id":-42,"message_type":"group","group_id":456,"user_id":123}}`)
					return
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || r.URL.Path != "/set_msg_emoji_like" || len(body) != 3 || body["message_id"] != "-42" || body["emoji_id"] != "76" || body["set"] != test.want {
					t.Errorf("unexpected reaction action: %s %+v", r.URL.Path, body)
				}
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"result":true}}`)
			}))
			defer server.Close()
			params := map[string]any{"message_id": "-42", "emoji_id": "76"}
			if test.set != nil {
				params["set"] = test.set
			}
			if _, err := napcatOperation(context.Background(), napcatTestAdapter(t, server), "set_reaction", domain.Conversation{ExternalID: "group:456"}, params); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNapCatPokeUsesOnlySelectedConversation(t *testing.T) {
	for _, test := range []struct {
		name, chat, target string
		params             map[string]any
	}{
		{"group member", "group:456", "123", map[string]any{"user_id": "123"}},
		{"private implicit peer", "private:123", "123", nil},
		{"private explicit peer", "private:123", "123", map[string]any{"user_id": "123"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid request body")
				}
				if r.URL.Path == "/get_group_member_info" {
					if body["group_id"] != "456" || body["user_id"] != test.target || body["no_cache"] != true {
						t.Errorf("group membership lookup escaped target: %+v", body)
					}
					fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"group_id":456,"user_id":123}}`)
					return
				}
				if r.URL.Path != "/send_poke" || body["user_id"] != test.target {
					t.Errorf("unexpected poke action: %s %+v", r.URL.Path, body)
				}
				if strings.HasPrefix(test.chat, "group:") {
					if len(body) != 2 || body["group_id"] != "456" {
						t.Errorf("group poke escaped selected conversation: %+v", body)
					}
				} else if len(body) != 1 {
					t.Errorf("private poke has alternate routing: %+v", body)
				}
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":null}`)
			}))
			defer server.Close()
			if _, err := napcatOperation(context.Background(), napcatTestAdapter(t, server), "poke", domain.Conversation{ExternalID: test.chat}, test.params); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNapCatReactionAndPokeInvalidParametersDoNotReachPlatform(t *testing.T) {
	n := &napcat{}
	for _, test := range []struct {
		name, chat string
		params     map[string]any
	}{
		{"set_reaction", "group:456", map[string]any{"message_id": "-42", "emoji_id": "76", "set": "false"}},
		{"set_reaction", "group:456", map[string]any{"message_id": "-42", "emoji_id": float64(76)}},
		{"set_reaction", "group:456", map[string]any{"message_id": "-42", "emoji_id": "../76"}},
		{"poke", "group:456", nil},
		{"poke", "group:456", map[string]any{"user_id": float64(1 << 54)}},
		{"poke", "group:456", map[string]any{"user_id": float64(1.5)}},
		{"poke", "group:456", map[string]any{"user_id": "0"}},
		{"poke", "private:123", map[string]any{"user_id": "789"}},
		{"send_shake", "private:123", nil},
	} {
		if _, err := napcatOperation(context.Background(), n, test.name, domain.Conversation{ExternalID: test.chat}, test.params); err == nil {
			t.Fatalf("accepted invalid or unsupported operation: %+v", test)
		}
	}
	for _, op := range chatOperations(n) {
		if op.Name == "send_shake" {
			t.Fatal("advertised unsupported NapCat window shake sending")
		}
		if op.Name == "set_reaction" && (!op.MessageScoped || !slices.Equal(op.Parameters, []string{"message_id", "emoji_id", "set"})) {
			t.Fatalf("reaction action lost message authorization: %+v", op)
		}
	}
}

func TestNapCatNewActionsPreserveUncertainFailure(t *testing.T) {
	for _, name := range []string{"poke", "set_reaction"} {
		var mutation atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/get_msg" {
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"message_id":-42,"message_type":"group","group_id":456,"user_id":123}}`)
				return
			}
			if r.URL.Path == "/get_group_member_info" {
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"group_id":456,"user_id":123}}`)
				return
			}
			mutation.Add(1)
			fmt.Fprint(w, `{"status":"failed","retcode":200}`)
		}))
		n := napcatTestAdapter(t, server)
		parameters := map[string]any{"user_id": "123"}
		if name == "set_reaction" {
			parameters = map[string]any{"message_id": "-42", "emoji_id": "76"}
		}
		_, err := napcatOperation(context.Background(), n, name, domain.Conversation{ExternalID: "group:456"}, parameters)
		var sendErr *SendError
		if mutation.Load() != 1 || !errors.As(err, &sendErr) || !sendErr.Unknown {
			t.Fatalf("%s failure could cause duplicate actions on retry: %v", name, err)
		}
		server.Close()
	}
}

func TestNapCatPrivateWindowShakeIsReadable(t *testing.T) {
	n := &napcat{connection: domain.Connection{Meta: domain.Meta{ID: "n"}, AccountID: "999"}}
	event, err := n.normalize(json.RawMessage(`{"post_type":"message","message_type":"private","self_id":999,"user_id":123,"message_id":2,"message":[{"type":"shake","data":{}}]}`))
	if err != nil || event == nil || event.Observation.Text != "[QQ private window shake]" || event.Conversation.ExternalID != "private:123" {
		t.Fatalf("window shake was lost: %+v %v", event, err)
	}
}

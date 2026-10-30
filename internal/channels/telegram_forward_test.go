package channels

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
)

func TestTelegramForwardOriginsPreserveSourcesAndEnclosingIdentity(t *testing.T) {
	originalTime := time.Unix(1792900000, 0).UTC()
	for _, test := range []struct {
		name, source string
		want         domain.ForwardOrigin
	}{
		{"user", `"sender_user":{"id":999,"first_name":"Original","last_name":"Author","username":"astral_bot"}`,
			domain.ForwardOrigin{Type: "user", Sender: domain.Sender{ID: "999", Name: "Original Author", Username: "astral_bot"}, SentAt: &originalTime}},
		{"hidden_user", `"sender_user_name":"Hidden <author>&","sender_user":{"id":999,"username":"astral_bot"}`,
			domain.ForwardOrigin{Type: "hidden_user", Sender: domain.Sender{Name: "Hidden <author>&"}, SentAt: &originalTime}},
		{"chat", `"sender_chat":{"id":-200,"type":"supergroup","title":"Source group","username":"source_group"},"author_signature":"Admin <name>&"`,
			domain.ForwardOrigin{Type: "chat", Sender: domain.Sender{ID: "chat:-200", Name: "Source group", Username: "source_group"}, SentAt: &originalTime, AuthorSignature: "Admin <name>&"}},
		{"channel", `"chat":{"id":-300,"type":"channel","title":"Source channel","username":"source_channel"},"message_id":501,"author_signature":"Editor <name>&"`,
			domain.ForwardOrigin{Type: "channel", Sender: domain.Sender{ID: "chat:-300", Name: "Source channel", Username: "source_channel"}, SentAt: &originalTime, MessageID: "501", AuthorSignature: "Editor <name>&"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			origin := fmt.Sprintf(`{"type":%q,"date":1792900000,%s}`, test.name, test.source)
			raw := fmt.Sprintf(`{"message_id":43,"date":1793000000,"edit_date":1793000001,"from":{"id":7,"first_name":"Forwarder"},"chat":{"id":-100,"type":"supergroup"},"caption":"forwarded photo","photo":[{"file_id":"photo","width":10,"height":10}],"forward_origin":%s,"reply_to_message":{"message_id":42,"from":{"id":8,"first_name":"Earlier forwarder"},"text":"quoted body","forward_origin":%s}}`, origin, origin)
			var message telegramMessage
			if err := json.Unmarshal([]byte(raw), &message); err != nil {
				t.Fatal(err)
			}
			adapter := &telegram{connection: domain.Connection{Meta: domain.Meta{ID: "account"}, AccountID: "999", Username: "astral_bot"}}
			for _, edited := range []bool{false, true} {
				event := adapter.normalize(1, message, edited)
				observation := event.Observation
				if !reflect.DeepEqual(observation.ForwardOrigin, &test.want) || !reflect.DeepEqual(observation.ReplyForwardOrigin, &test.want) {
					t.Fatalf("forwarded source was lost or conflated: current=%+v reply=%+v", observation.ForwardOrigin, observation.ReplyForwardOrigin)
				}
				if observation.Sender.ID != "7" || observation.Sender.Name != "Forwarder" || observation.SentAt.Unix() != 1793000000 || observation.MessageID != "43" || event.Conversation.ExternalID != "-100" || observation.ReplyID != "42" || observation.ReplySender != "Earlier forwarder" || observation.ReplyPreview != "quoted body" {
					t.Fatal("forwarded source replaced the enclosing message, chat or reply identity")
				}
				if observation.Text != "forwarded photo" || len(observation.Attachments) != 1 || observation.Attachments[0].ExternalID != "photo" || observation.Trigger != "" || observation.Self || len(observation.Parts) != 0 {
					t.Fatal("forwarded metadata changed media, trigger, self or merged-forward semantics")
				}
				if edited && (observation.Kind != "message.edited" || observation.EditedAt == nil || observation.EditedAt.Unix() != 1793000001) {
					t.Fatal("forwarded source changed edit identity")
				}
			}
			message.Reply.From.ID = 999
			if adapter.normalize(2, message, false).Observation.Trigger != "reply" {
				t.Fatal("a source origin suppressed an actual reply to the Bot")
			}
		})
	}
}

func TestTelegramForwardOriginDoesNotInventUnavailableFields(t *testing.T) {
	for _, source := range []*telegramMessageOrigin{nil, {Type: "future"}, {Type: "user"}, {Type: "chat"}, {Type: "channel"}} {
		if got := telegramForwardOrigin(source); got != nil {
			t.Fatalf("absent or unsupported source invented metadata: %+v", got)
		}
	}
	origin := telegramForwardOrigin(&telegramMessageOrigin{Type: "hidden_user", SenderUserName: "Hidden author"})
	if origin == nil || origin.Sender.ID != "" || origin.SentAt != nil || origin.MessageID != "" {
		t.Fatal("hidden source invented an identity, timestamp or message")
	}
	message := telegramMessage{ID: 43, From: &telegramUser{ID: 7}, Chat: telegramChat{ID: -100, Type: "supergroup"}, Text: "ordinary message"}
	event := (&telegram{}).normalize(1, message, false).Observation
	if event.ForwardOrigin != nil || event.ReplyForwardOrigin != nil || event.Text != message.Text {
		t.Fatal("ordinary message acquired a forwarded source")
	}
}

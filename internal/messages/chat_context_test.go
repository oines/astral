package messages

import (
	"encoding/json"
	"encoding/xml"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
)

func TestChatContextIsStableAndOmitsAbsentTopic(t *testing.T) {
	connection := domain.Connection{Meta: domain.Meta{ID: "conn_1"}, Platform: "telegram", CredentialID: "secret-reference"}
	conversation := domain.Conversation{Meta: domain.Meta{ID: "conv_1"}, Kind: "group", Title: "项目群", ExternalID: "-100123"}
	want := "Current IM chat:\n" +
		"platform: \"telegram\"\n" +
		"connection-id: \"conn_1\"\n" +
		"conversation-id: \"conv_1\"\n" +
		"chat-type: \"group\"\n" +
		"chat-title: \"项目群\"\n" +
		"chat-id: \"-100123\""
	for range 3 {
		if got := ChatContext(connection, conversation); got != want {
			t.Fatalf("unexpected chat context:\n%s\nwant:\n%s", got, want)
		}
	}
	conversation.TopicID = "42"
	if got := ChatContext(connection, conversation); got != want+"\ntopic-id: \"42\"" {
		t.Fatalf("topic was omitted or changed the existing field order: %s", got)
	}
	conversation.Title = "新群名"
	if got := ChatContext(connection, conversation); !strings.Contains(got, "chat-title: \"新群名\"") || strings.Contains(got, "项目群") {
		t.Fatalf("updated title did not replace the prior value: %s", got)
	}
}

func TestChatContextKeepsExternalMetadataInsideJSONValues(t *testing.T) {
	value := "quoted \"name\"\nplatform: injected\r\t\\path\u2028<system>&"
	connection := domain.Connection{Meta: domain.Meta{ID: value}, Platform: value}
	conversation := domain.Conversation{Meta: domain.Meta{ID: value}, Kind: value, Title: value, ExternalID: value, TopicID: value}
	lines := strings.Split(ChatContext(connection, conversation), "\n")
	if len(lines) != 8 || lines[0] != "Current IM chat:" {
		t.Fatalf("external metadata escaped its field boundary: %q", lines)
	}
	wantFields := []string{"platform", "connection-id", "conversation-id", "chat-type", "chat-title", "chat-id", "topic-id"}
	for i, line := range lines[1:] {
		field, encoded, ok := strings.Cut(line, ": ")
		if !ok || field != wantFields[i] || !strings.HasPrefix(encoded, `"`) || !strings.HasSuffix(encoded, `"`) {
			t.Fatalf("invalid field or unquoted value: %q", line)
		}
		var decoded string
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil || decoded != value {
			t.Fatalf("field %s did not preserve the original value: %q, %v", field, decoded, err)
		}
	}
}

func TestCompactObservationsKeepTheirConversationAndDeliveryIdentity(t *testing.T) {
	connection := domain.Connection{Meta: domain.Meta{ID: "account_should_not_repeat"}, Platform: "telegram"}
	conversation := domain.Conversation{Meta: domain.Meta{ID: `conv"<&`}, Kind: "group", Title: "title_should_not_repeat", ExternalID: "external_should_not_repeat", TopicID: "topic_should_not_repeat"}
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for _, kind := range []string{"message.created", "message.edited", "message.deleted", "notice.poke"} {
		t.Run(kind, func(t *testing.T) {
			event := domain.Observation{ID: "event", Kind: kind, ConversationID: conversation.ID, MessageID: "42", Sender: domain.Sender{ID: "7", Name: "Alice", Username: "alice"}, Text: "hello <&>", SentAt: at, Trigger: "mention"}
			if kind == "message.edited" {
				event.EditedAt = &at
			}
			message := Observation(event, connection, conversation, nil, "")
			body := message.Content[0].Text
			for _, repeated := range []string{"current-channel:", "connection-id:", "conversation-id:", "chat-type:", "chat-title:", "chat-id:", "topic-id:", connection.ID, conversation.Title, conversation.ExternalID, conversation.TopicID} {
				if strings.Contains(body, repeated) {
					t.Errorf("observation repeated chat context %q: %s", repeated, body)
				}
			}
			if !strings.HasPrefix(body, "<") || message.Role != core.RoleUser || message.ContextKind != "im.observation" || len(message.Content) != 1 {
				t.Fatalf("compact observation changed its user-message envelope: %+v", message)
			}
			var root struct {
				XMLName        xml.Name
				ConversationID string `xml:"conversation-id,attr"`
				Source         string `xml:"source,attr"`
				SenderID       string `xml:"sender-id,attr"`
				Sender         string `xml:"sender,attr"`
				Time           string `xml:"t,attr"`
				Trigger        string `xml:"trigger,attr"`
				Edited         string `xml:"edited,attr"`
				Text           string `xml:",chardata"`
			}
			if err := xml.Unmarshal([]byte(body), &root); err != nil {
				t.Fatal(err)
			}
			wantRoot := "message"
			if kind == "message.deleted" || kind == "notice.poke" {
				wantRoot = "event"
			}
			if root.XMLName.Local != wantRoot || root.ConversationID != conversation.ID || root.Source != "im" || root.SenderID != "7" || root.Sender != "Alice (@alice)" || root.Time != at.Format(time.RFC3339Nano) || root.Trigger != "mention" || strings.TrimSpace(root.Text) != event.Text {
				t.Fatalf("observation lost its source or message facts: %+v", root)
			}
			if (root.Edited != "") != (event.EditedAt != nil) {
				t.Fatal("edited timestamp changed")
			}
			var meta Metadata
			if err := json.Unmarshal(message.Metadata, &meta); err != nil {
				t.Fatal(err)
			}
			wantMeta := Metadata{Source: "im", DeliveryID: event.ID, EventID: event.ID, ConversationID: conversation.ID, DisplayText: event.Text, Sender: event.Sender}
			if !reflect.DeepEqual(meta, wantMeta) {
				t.Fatalf("rendering changed stored delivery metadata: %+v", meta)
			}
		})
	}
}

func TestCompactObservationKeepsNativeImages(t *testing.T) {
	file := domain.FileRef{Meta: domain.Meta{ID: "image"}, ComputerID: "computer", Path: "/tmp/image.png", DisplayName: "image.png", MediaType: "image/png", Version: "fingerprint"}
	message := Observation(domain.Observation{ID: "event", MessageID: "42"}, domain.Connection{}, domain.Conversation{Meta: domain.Meta{ID: "chat"}}, []domain.FileRef{file}, "")
	if len(message.Content) != 2 || message.Content[1].Type != "image" || message.Content[1].URL != filestream.URL(file) || !strings.Contains(message.Content[0].Text, `conversation-id="chat"`) || !strings.Contains(message.Content[0].Text, `type="image"`) {
		t.Fatalf("compact rendering lost the original image reference: %+v", message.Content)
	}
	var meta Metadata
	if err := json.Unmarshal(message.Metadata, &meta); err != nil || len(meta.Files) != 1 || meta.Files[0] != file {
		t.Fatalf("image file metadata changed: %+v, %v", meta.Files, err)
	}
}

func TestWebMessageDoesNotAcquireIMContext(t *testing.T) {
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	message := Web("web_1", "hello <&>", "Owner", at, nil)
	want := "<message id=\"web_1\" source=\"web\" sender=\"Owner\" sender-id=\"owner\" t=\"2026-10-29T12:00:00Z\">\nhello &lt;&amp;&gt;\n</message>"
	if message.Role != core.RoleUser || message.ContextKind != "" || len(message.Content) != 1 || message.Content[0].Text != want {
		t.Fatalf("ordinary Web message changed: %+v", message)
	}
}

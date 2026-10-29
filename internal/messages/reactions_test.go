package messages

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
)

func TestReactionContextPreservesFactsAndUnknownValues(t *testing.T) {
	zero, twelve, reported := int64(0), int64(12), int64(1)
	for _, test := range []struct {
		name, kind, actor string
		sender            domain.Sender
		self              bool
		reactions         []domain.Reaction
	}{
		{"user add", "message.reaction.added", "user", domain.Sender{ID: "7", Name: "Alice"}, false, []domain.Reaction{{Type: "emoji", Emoji: "👍"}}},
		{"Feishu removal", "message.reaction.removed", "user", domain.Sender{ID: "ou_7"}, false, []domain.Reaction{{Type: "feishu", ID: "THUMBSUP"}}},
		{"anonymous totals", "message.reaction.counts_updated", "unknown", domain.Sender{}, false, []domain.Reaction{{Type: "emoji", Emoji: "👍", Count: &twelve}, {Type: "emoji", Emoji: "🎉", Count: &zero}}},
		{"QQ unknown actor", "message.reaction.added", "unknown", domain.Sender{}, false, []domain.Reaction{{Type: "qq", ID: "76", ReportedCount: &reported}}},
		{"own clear", "message.reaction.set", "user", domain.Sender{ID: "bot"}, true, []domain.Reaction{}},
		{"escaped identifier", "message.reaction.added", "chat", domain.Sender{ID: "chat:1"}, false, []domain.Reaction{{Type: "custom_emoji", ID: `id"/><injected/>`}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := domain.Observation{ID: "event", Kind: test.kind, MessageID: "101", Sender: test.sender, Self: test.self, SentAt: time.Now(), Text: "<untrusted>", Reaction: &domain.ReactionEvent{ActorType: test.actor, Reactions: test.reactions}}
			message := Observation(event, domain.Connection{}, domain.Conversation{Meta: domain.Meta{ID: "conv_1"}}, nil, "")
			body := message.Content[0].Text
			var root struct {
				XMLName        xml.Name
				Kind           string `xml:"kind,attr"`
				ConversationID string `xml:"conversation-id,attr"`
				MessageID      string `xml:"message-id,attr"`
				Actor          string `xml:"actor-type,attr"`
				Known          string `xml:"actor-known,attr"`
				Self           string `xml:"self,attr"`
				Sender         string `xml:"sender,attr"`
				SenderID       string `xml:"sender-id,attr"`
				Reactions      []struct {
					Type     string `xml:"type,attr"`
					Emoji    string `xml:"emoji,attr"`
					ID       string `xml:"id,attr"`
					Count    *int64 `xml:"count,attr"`
					Reported *int64 `xml:"reported-count,attr"`
				} `xml:"reactions>reaction"`
			}
			if err := xml.Unmarshal([]byte(body), &root); err != nil {
				t.Fatal(err)
			}
			if root.XMLName.Local != "event" || root.Kind != test.kind || root.ConversationID != "conv_1" || root.MessageID != "101" || root.Actor != test.actor || root.Sender != test.sender.Name || root.SenderID != test.sender.ID || (root.Self == "true") != test.self {
				t.Fatalf("reaction context lost its source or actor: %+v", root)
			}
			if test.sender.ID == "" && (root.Known != "false" || strings.Contains(body, "sender=")) {
				t.Fatal("an anonymous reaction acquired a fabricated sender")
			}
			if len(root.Reactions) != len(test.reactions) {
				t.Fatal("reaction items were dropped or invented")
			}
			for i, reaction := range root.Reactions {
				want := test.reactions[i]
				if reaction.Type != want.Type || reaction.Emoji != want.Emoji || reaction.ID != want.ID || (reaction.Count == nil) != (want.Count == nil) || (reaction.Reported == nil) != (want.ReportedCount == nil) {
					t.Fatalf("reaction identity or count meaning changed: %+v", reaction)
				}
				if reaction.Count != nil && *reaction.Count != *want.Count || reaction.Reported != nil && *reaction.Reported != *want.ReportedCount {
					t.Fatal("reaction count changed")
				}
			}
			if !strings.Contains(body, "&lt;untrusted&gt;") || strings.Contains(body, "<injected/>") || strings.Contains(body, "chat-title:") || strings.Contains(body, "current-channel:") || len(message.Content) != 1 {
				t.Fatal("reaction text escaped its envelope or repeated chat context")
			}
		})
	}
}

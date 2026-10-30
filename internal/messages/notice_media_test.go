package messages

import (
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestObservationReplyMediaKeepsOriginalIdentityWithoutNativePreview(t *testing.T) {
	event := domain.Observation{ID: "reply-event", MessageID: "43", Text: "current", ReplyID: "42", ReplySender: "Alice", ReplyPreview: "source caption",
		ReplyAttachments: []domain.Attachment{{Ref: "42:0", Kind: "image", Name: `photo<&>.png`, MediaType: "image/png", Size: 12}}}
	message := Observation(event, domain.Connection{}, domain.Conversation{Meta: domain.Meta{ID: "chat"}}, nil, "")
	body := message.Content[0].Text
	want := `<in-reply-to id="42" sender="Alice" read-url="` + MessagePartsReadPath("chat", "42") + `">source caption` + "\n" + `<attachment type="image" name="photo&lt;&amp;&gt;.png" mime="image/png" message-id="42" file-id="42:0" size="12" download-url="` + AttachmentDownloadPath("chat", "42", "42:0") + `"/></in-reply-to>`
	if !strings.Contains(body, want) || len(message.Content) != 1 || strings.Contains(body, AttachmentDownloadPath("chat", "43", "42:0")) {
		t.Fatal("reply media became a current attachment, acquired automatic pixels, or lost its source identity")
	}
}

func TestObservationNoticeUsesEscapedIndependentEvent(t *testing.T) {
	for _, kind := range []string{"message.deleted", "notice.poke"} {
		event := domain.Observation{ID: "notice", Kind: kind, Sender: domain.Sender{ID: "123", Name: `Alice<&>`}, Text: `operator <555> recalled/poked <message>&`}
		if kind == "message.deleted" {
			event.MessageID = "42"
		}
		body := Observation(event, domain.Connection{}, domain.Conversation{}, nil, "").Content[0].Text
		if !strings.Contains(body, `<event id="notice" kind="`+kind+`"`) || !strings.Contains(body, Escape(event.Text)+`</event>`) || strings.Contains(body, "<message ") || !strings.Contains(body, `sender="Alice&lt;&amp;&gt;"`) {
			t.Fatal("notice impersonated an ordinary user message or lost escaped facts")
		}
		if strings.Contains(body, `message-id="42"`) != (kind == "message.deleted") {
			t.Fatal("notice target message reference was lost or fabricated")
		}
	}
	trigger := Trigger(domain.Observation{ID: "notice", Trigger: "poke"}).Content[0].Text
	if !strings.Contains(trigger, `event-id="notice"`) || !strings.Contains(trigger, `reason="poke"`) || strings.Contains(trigger, "message-id=") {
		t.Fatal("poke trigger does not reference the independent notice")
	}
}

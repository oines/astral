package messages

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
)

func TestForwardOriginRenderingKeepsCurrentAndQuotedSourceBoundaries(t *testing.T) {
	originalTime := time.Unix(1792900000, 0).UTC()
	event := domain.Observation{
		ID: "event", MessageID: "43", Sender: domain.Sender{ID: "7", Name: "Forwarder"}, SentAt: time.Unix(1793000000, 0).UTC(), Text: "forwarded body",
		ForwardOrigin: &domain.ForwardOrigin{Type: "channel", Sender: domain.Sender{ID: "chat:-300", Name: `<source>&`, Username: "source_channel"}, SentAt: &originalTime, MessageID: "501", AuthorSignature: `Editor "name"<&>`},
		ReplyID:       "42", ReplySender: "Earlier forwarder", ReplyPreview: "earlier forwarded body", ReplyForwardOrigin: &domain.ForwardOrigin{Type: "hidden_user", Sender: domain.Sender{Name: `Hidden "author"<&>`}},
	}
	message := Observation(event, domain.Connection{Platform: "telegram"}, domain.Conversation{Meta: domain.Meta{ID: "chat"}}, nil, "")
	body := message.Content[0].Text
	current := `<forward-origin type="channel" sender="` + Escape(`<source>& (@source_channel)`) + `" sender-id="chat:-300" message-id="501" author-signature="` + Escape(`Editor "name"<&>`) + `" t="` + originalTime.Format(time.RFC3339Nano) + `"/>`
	reply := `<in-reply-to id="42" sender="Earlier forwarder" read-url="` + MessagePartsReadPath("chat", "42") + `"><forward-origin type="hidden_user" sender="` + Escape(`Hidden "author"<&>`) + `"/>` + "\nearlier forwarded body</in-reply-to>"
	if !strings.Contains(body, `sender="Forwarder" sender-id="7" t="`+event.SentAt.Format(time.RFC3339Nano)+`">`+"\n"+current+"\n"+reply+"\nforwarded body") || strings.Count(body, "<forward-origin ") != 2 {
		t.Fatalf("sources were lost, duplicated or moved outside their message: %s", body)
	}
	var metadata Metadata
	if err := json.Unmarshal(message.Metadata, &metadata); err != nil || !reflect.DeepEqual(metadata.ForwardOrigin, event.ForwardOrigin) || !reflect.DeepEqual(metadata.ReplyForwardOrigin, event.ReplyForwardOrigin) || metadata.Sender != event.Sender || metadata.DisplayText != event.Text {
		t.Fatal("display metadata lost sources or replaced the current author/body", err)
	}
	event.SentAt = event.SentAt.In(time.FixedZone("UTC+8", 8*60*60))
	body = Observation(event, domain.Connection{}, domain.Conversation{}, nil, "").Content[0].Text
	if !strings.Contains(body, `t="`+originalTime.In(event.SentAt.Location()).Format(time.RFC3339Nano)+`"`) || event.ForwardOrigin.SentAt.Location() != time.UTC {
		t.Fatal("source timestamp failed to use the display timezone or mutated the archived source")
	}
	event.ForwardOrigin, event.ReplyForwardOrigin = nil, nil
	message = Observation(event, domain.Connection{}, domain.Conversation{}, nil, "")
	if strings.Contains(message.Content[0].Text, "<forward-origin") || strings.Contains(string(message.Metadata), "forward_origin") {
		t.Fatal("ordinary message gained synthetic forward metadata")
	}
}

package gateway

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestForwardOriginsSurviveArchiveReopenAndMessageQueries(t *testing.T) {
	directory := t.TempDir()
	store, err := persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	at := time.Unix(1792900000, 0).UTC()
	event := domain.Observation{ID: "event", ExternalEventID: "10", ConnectionID: "account", ConversationID: "chat", MessageID: "43", Kind: "message.created", Text: "forwarded body", ReplyID: "42",
		ForwardOrigin:      &domain.ForwardOrigin{Type: "channel", Sender: domain.Sender{ID: "chat:-300", Name: "Source channel"}, SentAt: &at, MessageID: "501", AuthorSignature: "Editor"},
		ReplyForwardOrigin: &domain.ForwardOrigin{Type: "hidden_user", Sender: domain.Sender{Name: "Hidden author"}, SentAt: &at}}
	if _, err := store.Archive(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	message, err := store.Message(ctx, "chat", "43")
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.SearchObservations(ctx, "chat", 0, 20, "forwarded")
	if err != nil || len(page) != 1 {
		t.Fatal("archive list did not retain the message", err)
	}
	for _, observation := range []domain.Observation{message, page[0].Observation} {
		raw, err := json.Marshal(MessageObservation(observation))
		if err != nil {
			t.Fatal(err)
		}
		var view MessageObservationView
		if err := json.Unmarshal(raw, &view); err != nil || !reflect.DeepEqual(view.ForwardOrigin, event.ForwardOrigin) || !reflect.DeepEqual(view.ReplyForwardOrigin, event.ReplyForwardOrigin) || view.ConversationID != "chat" || view.MessageID != "43" || view.ReplyID != "42" {
			t.Fatalf("query dropped sources or substituted a source identity: %s, %v", raw, err)
		}
	}
	if _, err := store.Message(ctx, "chat", "501"); err == nil {
		t.Fatal("source channel message was fabricated in the current chat")
	}
	contract := New(nil, nil).Contract
	schema := contract.Schemas["astral_internal_gateway_MessageObservationView"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	for _, field := range []string{"forward_origin", "reply_forward_origin"} {
		if properties[field].(map[string]any)["$ref"] != "#/components/schemas/astral_internal_domain_ForwardOrigin" || slices.Contains(schema["required"].([]string), field) {
			t.Fatalf("source %s was omitted from the API schema or made mandatory", field)
		}
	}
}

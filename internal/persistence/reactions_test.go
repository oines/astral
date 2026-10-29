package persistence_test

import (
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestReactionFactsDoNotReplaceMessageStateOrRewriteHistory(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := t.Context()
	appendEvent := func(id, kind, text string) domain.Observation {
		t.Helper()
		event := domain.Observation{SchemaVersion: 1, ID: id, ExternalEventID: id, ConnectionID: "account", ConversationID: "chat", MessageID: "101", Kind: kind, Text: text}
		if kind == "message.reaction.added" || kind == "message.reaction.removed" {
			event.Reaction = &domain.ReactionEvent{ActorType: "user", Reactions: []domain.Reaction{{Type: "emoji", Emoji: "👍"}}}
		}
		inserted, err := store.Archive(ctx, event)
		if err != nil || !inserted {
			t.Fatalf("append %s: inserted=%v err=%v", id, inserted, err)
		}
		return event
	}
	original := appendEvent("created", "message.created", "original")
	appendEvent("edited", "message.edited", "edited")
	added := appendEvent("added", "message.reaction.added", "reaction")
	message, err := store.Message(ctx, "chat", "101")
	if err != nil || message.Kind != "message.edited" || message.Text != "edited" {
		t.Fatalf("reaction replaced the latest message: %+v, %v", message, err)
	}
	appendEvent("deleted", "message.deleted", "removed by a moderator")
	appendEvent("reaction-removed", "message.reaction.removed", "reaction removed")
	message, err = store.Message(ctx, "chat", "101")
	if err != nil || message.Kind != "message.deleted" {
		t.Fatalf("later reaction resurrected a deleted message: %+v, %v", message, err)
	}
	if duplicate, err := store.Archive(ctx, added); err != nil || duplicate {
		t.Fatal("a redelivered reaction was not deduplicated")
	}
	history, err := store.Observations(ctx, "chat", 0, 100)
	if err != nil || len(history) != 5 || history[0].Observation.ID != original.ID || history[0].Observation.Text != original.Text {
		t.Fatalf("appending a reaction changed the original history: %+v, %v", history, err)
	}
}

func TestMessageSourceLookupPreservesAccountChatAndTopicBoundaries(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := t.Context()
	saveSource := func(id, account, external, topic, messageID, kind string) domain.Conversation {
		t.Helper()
		chat := domain.Conversation{Meta: domain.Meta{ID: id}, ConnectionID: account, ExternalID: external, TopicID: topic, Kind: "topic"}
		if err := store.Save(ctx, "conversations", &chat.Meta, &chat); err != nil {
			t.Fatal(err)
		}
		event := domain.Observation{ID: id + ":event", ExternalEventID: id + ":event", ConnectionID: account, ConversationID: id, MessageID: messageID, Kind: kind}
		if _, err := store.Archive(ctx, event); err != nil {
			t.Fatal(err)
		}
		return chat
	}
	topic := saveSource("topic-A", "account-A", "-100", "42", "101", "message.created")
	saveSource("other-chat", "account-A", "-200", "", "101", "message.created")
	saveSource("foreign-account", "account-B", "-100", "42", "101", "message.created")
	saveSource("reaction-only", "account-A", "-100", "43", "999", "message.reaction.added")
	chat, err := store.MessageConversation(ctx, "account-A", "-100", "101")
	if err != nil || chat.ID != topic.ID || chat.TopicID != "42" {
		t.Fatalf("source escaped its account, external chat or Topic: %+v, %v", chat, err)
	}
	if _, err := store.MessageConversation(ctx, "account-A", "", "101"); err == nil {
		t.Fatal("ambiguous chat-local identity selected an arbitrary source")
	}
	if _, err := store.MessageConversation(ctx, "account-A", "-100", "999"); !domain.IsMissing(err) {
		t.Fatalf("reaction invented an original message source: %v", err)
	}
	saveSource("unique-global", "account-A", "oc_chat", "", "om_unique", "message.deleted")
	chat, err = store.MessageConversation(ctx, "account-A", "", "om_unique")
	if err != nil || chat.ID != "unique-global" {
		t.Fatalf("a deleted message lost its known source: %+v, %v", chat, err)
	}
	saveSource("ambiguous-topic", "account-A", "-100", "44", "101", "message.created")
	if _, err := store.MessageConversation(ctx, "account-A", "-100", "101"); err == nil {
		t.Fatal("conflicting historical Topic membership selected an arbitrary Topic")
	}
}

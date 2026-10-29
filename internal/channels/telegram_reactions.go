package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
)

type telegramReactionType struct {
	Type          string `json:"type"`
	Emoji         string `json:"emoji,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
}

func (r telegramReactionType) reaction() domain.Reaction {
	return domain.Reaction{Type: r.Type, Emoji: r.Emoji, ID: r.CustomEmojiID}
}

type telegramMessageReaction struct {
	Chat        telegramChat           `json:"chat"`
	MessageID   int64                  `json:"message_id"`
	User        *telegramUser          `json:"user"`
	ActorChat   *telegramChat          `json:"actor_chat"`
	Date        int64                  `json:"date"`
	OldReaction []telegramReactionType `json:"old_reaction"`
	NewReaction []telegramReactionType `json:"new_reaction"`
}

type telegramReactionCount struct {
	Type       telegramReactionType `json:"type"`
	TotalCount int64                `json:"total_count"`
}

type telegramMessageReactionCount struct {
	Chat      telegramChat            `json:"chat"`
	MessageID int64                   `json:"message_id"`
	Date      int64                   `json:"date"`
	Reactions []telegramReactionCount `json:"reactions"`
}

// Telegram's reaction updates contain no Topic identity. The poll service
// resolves their requested message against the archive before admitting them
// to a conversation; the adapter must never guess a Topic from a message ID.
func (t *telegram) reactionEvent(updateID int64, chat telegramChat, messageID, date int64, suffix string) Event {
	conversation := conversationFromChat(chat, "")
	conversation.ConnectionID = t.connection.ID
	conversation.ID = stableID("conversation", t.connection.ID, conversation.ExternalID, "")
	externalID := strconv.FormatInt(updateID, 10) + ":reaction:" + suffix
	observation := domain.Observation{
		SchemaVersion: 1, ID: stableID("event", t.connection.ID, externalID),
		ConnectionID: t.connection.ID, ConversationID: conversation.ID,
		ExternalEventID: externalID, Kind: "message.reaction." + suffix,
		MessageID: strconv.FormatInt(messageID, 10), ReceivedAt: time.Now().UTC(),
	}
	if date != 0 {
		observation.SentAt = time.Unix(date, 0).UTC()
	}
	return Event{Conversation: conversation, Observation: observation}
}

func telegramReactionDifference(previous, next []telegramReactionType) []domain.Reaction {
	known := make(map[telegramReactionType]bool, len(previous)+len(next))
	for _, reaction := range previous {
		known[reaction] = true
	}
	result := []domain.Reaction{}
	for _, reaction := range next {
		if !known[reaction] {
			result = append(result, reaction.reaction())
			known[reaction] = true
		}
	}
	return result
}

func (t *telegram) normalizeReaction(updateID int64, update telegramMessageReaction) []Event {
	actorType := "unknown"
	var sender domain.Sender
	if update.User != nil {
		actorType = "user"
		sender = domain.Sender{ID: strconv.FormatInt(update.User.ID, 10), Name: strings.TrimSpace(update.User.FirstName + " " + update.User.LastName), Username: update.User.Username}
	} else if update.ActorChat != nil {
		actorType = "chat"
		sender = domain.Sender{ID: "chat:" + strconv.FormatInt(update.ActorChat.ID, 10), Name: update.ActorChat.Title, Username: update.ActorChat.Username}
	}
	result := []Event{}
	for _, change := range []struct {
		kind      string
		reactions []domain.Reaction
	}{
		{"removed", telegramReactionDifference(update.NewReaction, update.OldReaction)},
		{"added", telegramReactionDifference(update.OldReaction, update.NewReaction)},
	} {
		if len(change.reactions) == 0 {
			continue
		}
		event := t.reactionEvent(updateID, update.Chat, update.MessageID, update.Date, change.kind)
		event.Observation.Sender = sender
		event.Observation.Self = actorType == "user" && sender.ID == t.connection.AccountID
		event.Observation.Reaction = &domain.ReactionEvent{ActorType: actorType, Reactions: change.reactions}
		result = append(result, event)
	}
	return result
}

func (t *telegram) normalizeReactionCount(updateID int64, update telegramMessageReactionCount) Event {
	event := t.reactionEvent(updateID, update.Chat, update.MessageID, update.Date, "counts_updated")
	reactions := make([]domain.Reaction, 0, len(update.Reactions))
	for _, count := range update.Reactions {
		reaction := count.Type.reaction()
		total := count.TotalCount
		reaction.Count = &total
		reactions = append(reactions, reaction)
	}
	event.Observation.Reaction = &domain.ReactionEvent{ActorType: "unknown", Reactions: reactions}
	return event
}

// setMessageReaction returns only a boolean and is not echoed through
// message_reaction. Record the accepted selection, without inventing a previous
// selection or claiming the requested ID is the actual media-group target.
func (s *Service) archiveTelegramReaction(ctx context.Context, t *telegram, target domain.Conversation, parameters map[string]any) error {
	var selected []telegramReactionType
	if raw := parameters["reaction"]; raw != nil {
		payload := encoded(raw)
		// Named Bot API parameters can also use Telegram's JSON-serialized
		// array spelling, including when the request uses multipart encoding.
		if serialized, ok := raw.(string); ok {
			payload = []byte(serialized)
		}
		if err := json.Unmarshal(payload, &selected); err != nil {
			return domain.Invalid("accepted Telegram reaction selection could not be archived")
		}
	}
	reactions := make([]domain.Reaction, 0, len(selected))
	for _, reaction := range selected {
		reactions = append(reactions, reaction.reaction())
	}
	messageID := telegramID(parameters["message_id"])
	now := time.Now().UTC()
	externalID := domain.ID("operation")
	observation := domain.Observation{
		SchemaVersion: 1, ID: stableID("event", t.connection.ID, externalID),
		ConnectionID: t.connection.ID, ConversationID: target.ID,
		ExternalEventID: externalID, Kind: "message.reaction.set", MessageID: messageID,
		Sender: domain.Sender{ID: t.connection.AccountID, Username: t.connection.Username},
		SentAt: now, ReceivedAt: now, Self: true,
		Text:     fmt.Sprintf("Telegram accepted the bot's reaction selection for requested message %s. For a media group, Telegram applies it to the first non-deleted message in the group.", messageID),
		Reaction: &domain.ReactionEvent{ActorType: "user", Reactions: reactions},
	}
	_, err := s.archiveEvent(ctx, observation)
	return err
}

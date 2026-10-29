package channels

import (
	"context"
	"errors"
	"log/slog"

	"github.com/oines/astral/internal/domain"
)

// routeReaction resolves chat-local message identities before inbox delivery.
// In particular Telegram reactions omit their Topic, so an account-level update
// cannot by itself grant or select any Topic. Unknown messages may only fall
// back to an authorized parent chat, never to each of its Topic bindings.
func (s *Service) routeReaction(ctx context.Context, connectionID string, event Event) (Event, bool, error) {
	var connection domain.Connection
	if err := s.Store.Get(ctx, "connections", connectionID, &connection); err != nil {
		return event, false, err
	}
	if !connection.Enabled {
		return event, false, nil
	}
	if event.Conversation.ConnectionID != connectionID || event.Observation.ConnectionID != connectionID || event.Observation.MessageID == "" {
		return event, false, domain.Forbidden("reaction does not identify a message in its account")
	}
	chat, err := s.Store.MessageConversation(ctx, connectionID, event.Conversation.ExternalID, event.Observation.MessageID)
	if err != nil {
		if !domain.IsMissing(err) {
			// An ambiguous historical source cannot safely choose a Topic. Keep
			// other updates moving rather than retrying the same unsafe route.
			var conflict *domain.Error
			if !errors.As(err, &conflict) || conflict.Code != "conflict" {
				return event, false, err
			}
			slog.WarnContext(ctx, "Reaction source could not be resolved", "connection_id", connectionID, "message_id", event.Observation.MessageID)
			return event, false, nil
		}
		chat = event.Conversation
		var saved domain.Conversation
		if lookupErr := s.Store.Get(ctx, "conversations", chat.ID, &saved); lookupErr == nil {
			chat = saved
		} else if !domain.IsMissing(lookupErr) {
			return event, false, lookupErr
		}
	}
	if chat.ConnectionID != connectionID {
		return event, false, domain.Forbidden("reaction chat belongs to another account")
	}
	if connection.AccessMode != "all" {
		binding, err := s.activeBinding(ctx, chat.ID)
		if err != nil {
			return event, false, err
		}
		if binding.ID == "" || binding.AgentID != connection.AgentID {
			return event, false, nil
		}
	}
	event.Conversation, event.Observation.ConversationID = chat, chat.ID
	return event, true, nil
}

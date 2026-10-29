package channels

import (
	"context"
	"errors"
	"strconv"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func (f *feishu) setStore(store *persistence.Store) { f.store = store }

func (f *feishu) receiveReaction(ctx context.Context, stream *feishuStream, base *larkevent.EventV2Base, data *larkim.P2MessageReactionCreatedV1Data, removed bool) error {
	event, err := f.normalizeReaction(ctx, base, data, removed)
	if err != nil || event == nil {
		return err
	}
	return stream.deliver(ctx, *event)
}

// Reaction callbacks carry a globally addressed message ID, but no chat ID.
// Resolve archived messages first, including messages subsequently deleted, so
// their reactions retain their original chat without fetching message bodies.
func (f *feishu) reactionConversation(ctx context.Context, messageID string) (domain.Conversation, error) {
	if f.store != nil {
		conversation, err := f.store.MessageConversation(ctx, f.connection.ID, "", messageID)
		if err == nil || !domain.IsMissing(err) {
			return conversation, err
		}
	}
	message, err := f.getMessage(ctx, messageID)
	if err != nil {
		return domain.Conversation{}, err
	}
	return f.ResolveConversation(ctx, message.ChatID, "")
}

func (f *feishu) normalizeReaction(ctx context.Context, base *larkevent.EventV2Base, data *larkim.P2MessageReactionCreatedV1Data, removed bool) (*Event, error) {
	if base == nil || base.Header == nil || base.Header.EventID == "" || data == nil || data.ReactionType == nil {
		return nil, domain.Invalid("Feishu delivered an incomplete reaction event")
	}
	if base.Header.AppID != f.config.AppID {
		return nil, domain.Forbidden("Feishu reaction event belongs to another application")
	}
	messageID, emojiType := feishuValue(data.MessageId), feishuValue(data.ReactionType.EmojiType)
	if !feishuID(messageID) || !feishuID(emojiType) || len(emojiType) > 128 {
		return nil, domain.Invalid("Feishu reaction event has invalid message or emoji IDs")
	}
	actorType, actorID := feishuValue(data.OperatorType), ""
	switch actorType {
	case "user":
		if data.UserId != nil {
			actorID = feishuValue(data.UserId.OpenId)
		}
	case "app":
		actorID = feishuValue(data.AppId)
	default:
		return nil, domain.Invalid("Feishu reaction event has an unsupported operator type")
	}
	if actorID != "" && !feishuID(actorID) {
		return nil, domain.Invalid("Feishu reaction event has an invalid operator ID")
	}
	millis, err := strconv.ParseInt(feishuValue(data.ActionTime), 10, 64)
	if err != nil || millis <= 0 {
		return nil, domain.Invalid("Feishu reaction event has an invalid timestamp")
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conversation, err := f.reactionConversation(resolveCtx, messageID)
	if err != nil {
		// An unknown message that has permanently disappeared cannot be routed.
		// ACK that event instead of repeatedly retrying it; permission failures
		// and transient errors still ask Feishu to redeliver. No pending queue
		// entry is created, so another message can continue through the stream.
		var api *feishuAPIError
		if errors.As(err, &api) && (api.Code == 230110 || api.Code == 231003) {
			return nil, nil
		}
		return nil, err
	}
	if conversation.ConnectionID != f.connection.ID || !feishuID(conversation.ExternalID) {
		return nil, domain.Forbidden("Feishu reaction resolved outside its account")
	}
	kind := "message.reaction.added"
	if removed {
		kind = "message.reaction.removed"
	}
	observation := domain.Observation{
		SchemaVersion: 1, ID: stableID("event", f.connection.ID, base.Header.EventID),
		ConnectionID: f.connection.ID, ConversationID: conversation.ID, ExternalEventID: base.Header.EventID,
		Kind: kind, MessageID: messageID, Sender: domain.Sender{ID: actorID},
		SentAt: time.UnixMilli(millis).UTC(), ReceivedAt: time.Now().UTC(),
		Self:     actorType == "app" && actorID == f.config.AppID,
		Reaction: &domain.ReactionEvent{ActorType: actorType, Reactions: []domain.Reaction{{Type: "feishu", ID: emojiType}}},
	}
	return &Event{Conversation: conversation, Observation: observation}, nil
}

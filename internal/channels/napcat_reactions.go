package channels

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
)

// NapCat reports group reactions as notices, not edits of the target message.
// Some versions omit the actor on reactions to another member's message, and
// their reported count is not consistently the total number of reactions.
func (n *napcat) normalizeReaction(raw json.RawMessage) (*Event, error) {
	var notice struct {
		EventID   qqValue `json:"event_id"`
		GroupID   qqValue `json:"group_id"`
		GroupName string  `json:"group_name"`
		UserID    qqValue `json:"user_id"`
		MessageID qqValue `json:"message_id"`
		Time      int64   `json:"time"`
		IsAdd     *bool   `json:"is_add"`
		Likes     []struct {
			ID    qqValue `json:"emoji_id"`
			Count *int64  `json:"count"`
		} `json:"likes"`
	}
	if json.Unmarshal(raw, &notice) != nil || !napcatNumericID(string(notice.GroupID)) || notice.MessageID == "" || notice.IsAdd == nil || len(notice.Likes) == 0 {
		return nil, domain.Invalid("NapCat reaction did not identify its group, message or change")
	}
	actor := string(notice.UserID)
	actorType := "user"
	if actor == "" || actor == "0" {
		actor, actorType = "", "unknown"
	} else if !napcatNumericID(actor) {
		return nil, domain.Invalid("NapCat reaction returned an invalid actor")
	}
	reactions := make([]domain.Reaction, 0, len(notice.Likes))
	ids := make([]string, 0, len(notice.Likes))
	for _, like := range notice.Likes {
		if strings.TrimSpace(string(like.ID)) == "" || like.Count != nil && *like.Count < 0 {
			return nil, domain.Invalid("NapCat reaction returned an invalid emoji or reported count")
		}
		reactions = append(reactions, domain.Reaction{Type: "qq", ID: string(like.ID), ReportedCount: like.Count})
		ids = append(ids, string(like.ID))
	}
	conversation := domain.Conversation{ConnectionID: n.connection.ID, ExternalID: "group:" + string(notice.GroupID), Kind: "group", Title: string(notice.GroupID)}
	if notice.GroupName != "" {
		conversation.Title = notice.GroupName
	}
	conversation.ID = stableID("conversation", n.connection.ID, conversation.ExternalID, "")
	kind, action := "message.reaction.added", "added"
	if !*notice.IsAdd {
		kind, action = "message.reaction.removed", "removed"
	}
	// OneBot has no replay cursor or reliable reaction ID. Allocate once per
	// received frame; Poll's pending Batch preserves it across delivery retries.
	externalEvent := domain.ID("notice")
	if notice.EventID != "" {
		externalEvent = kind + ":" + conversation.ExternalID + ":" + string(notice.EventID)
	}
	now := time.Now().UTC()
	sentAt := now
	if notice.Time > 0 {
		sentAt = time.Unix(notice.Time, 0).UTC()
	}
	actorText := "An unknown QQ user"
	if actor != "" {
		actorText = "QQ user " + actor
	}
	observation := domain.Observation{SchemaVersion: 1, ID: stableID("event", n.connection.ID, externalEvent),
		ConnectionID: n.connection.ID, ConversationID: conversation.ID, ExternalEventID: externalEvent,
		Kind: kind, MessageID: string(notice.MessageID), Sender: domain.Sender{ID: actor},
		Text:   fmt.Sprintf("%s %s QQ reaction %s on message %s.", actorText, action, strings.Join(ids, ", "), notice.MessageID),
		SentAt: sentAt, ReceivedAt: now, Self: actor != "" && actor == n.connection.AccountID,
		Reaction: &domain.ReactionEvent{ActorType: actorType, Reactions: reactions}}
	return &Event{Conversation: conversation, Observation: observation}, nil
}

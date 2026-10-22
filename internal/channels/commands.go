package channels

import (
	"context"
	"errors"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

// Utility replies use their own receipts: they do not authorize a conversation,
// create a Session, enter model context, or expose an unscoped send API.
type commandReceipt struct {
	domain.Meta
	ConnectionID       string   `json:"connection_id"`
	ConversationID     string   `json:"conversation_id"`
	MessageID          string   `json:"message_id"`
	Name               string   `json:"name"`
	Text               string   `json:"text"`
	Status             string   `json:"status"`
	PlatformMessageIDs []string `json:"platform_message_ids,omitempty"`
	Error              string   `json:"error,omitempty"`
}

func (s *Service) recoverCommands(ctx context.Context) error {
	records, err := list[commandReceipt](ctx, s.Store, "channel_commands")
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Status != "dispatching" {
			continue
		}
		record.Status, record.Error = "unknown", "Server stopped before a platform receipt was committed"
		if err = s.Store.Save(ctx, "channel_commands", &record.Meta, &record); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) replyToCommand(ctx context.Context, adapter Adapter, event Event) error {
	if event.Command.Text == "" {
		return nil
	}
	observation := event.Observation
	record := commandReceipt{
		Meta:         domain.Meta{ID: stableID("command", observation.ConnectionID, observation.ConversationID, observation.MessageID)},
		ConnectionID: observation.ConnectionID, ConversationID: observation.ConversationID, MessageID: observation.MessageID,
		Name: event.Command.Name, Text: event.Command.Text, Status: "dispatching",
	}
	claimed := false
	err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var prior commandReceipt
		if err := tx.Get(ctx, "channel_commands", record.ID, &prior); !domain.IsMissing(err) {
			return err
		}
		if err := tx.Save(ctx, "channel_commands", &record.Meta, &record); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return err
	}
	// Persist the attempt before sending. Replayed updates and interrupted
	// requests must never cause an automatic second reply.
	receipt, sendErr := adapter.Send(ctx, Send{Target: event.Conversation, Text: record.Text, ReplyTo: observation.MessageID})
	record.Status, record.PlatformMessageIDs = "accepted", receipt.MessageIDs
	if sendErr != nil {
		record.Status, record.Error = "unknown", sendErr.Error()
		var known *SendError
		if errors.As(sendErr, &known) && !known.Unknown {
			record.Status = "failed"
		}
	} else if len(receipt.MessageIDs) == 0 {
		record.Status, record.Error = "unknown", "platform response did not identify an accepted message"
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.Store.Save(persist, "channel_commands", &record.Meta, &record); err != nil {
		return err
	}
	return sendErr
}

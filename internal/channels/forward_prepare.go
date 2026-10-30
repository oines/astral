package channels

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/oines/astral/internal/domain"
)

// One observation shares this budget across its own parts and quoted reply.
// The caller supplies the preparation deadline, shared with image previews.
type forwardPreparationBudget struct {
	calls     int
	messages  int
	bytesLeft int64
}

func newForwardPreparationBudget() *forwardPreparationBudget {
	return &forwardPreparationBudget{calls: 4, messages: 12, bytesLeft: 4 << 20}
}

// prepareForwardParts operates only on an in-memory delivery copy. Local
// authorization/credential failures remain prerequisites; unavailable remote
// resources are optional quoted content and must not block the IM inbox.
func (s *Service) prepareForwardParts(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID string, parts []domain.MessagePart, budget *forwardPreparationBudget) ([]domain.MessagePart, error) {
	if len(parts) == 0 {
		return parts, nil
	}
	if budget == nil {
		budget = newForwardPreparationBudget()
	}
	var prepared []domain.MessagePart
	if err := json.Unmarshal(encoded(parts), &prepared); err != nil {
		return nil, err
	}
	var resolver *messagePartResolver
	defer func() {
		if resolver != nil {
			budget.calls, budget.bytesLeft = resolver.callsLeft, resolver.bytesLeft
		}
	}()
	resolve := func() error {
		if resolver != nil {
			return nil
		}
		_, chat, connection, err := s.target(ctx, b.AgentID, b.SessionID, conversationID)
		if err != nil {
			return &attachmentPrerequisiteError{err}
		}
		adapter, err := s.adapter(ctx, connection)
		if err != nil {
			return &attachmentPrerequisiteError{err}
		}
		if _, ok := adapter.(*napcat); !ok {
			return &attachmentPrerequisiteError{domain.Invalid("forward source is no longer a NapCat account")}
		}
		source, quoted, err := s.Store.MessagePartsSource(ctx, chat.ID, messageID)
		if err != nil {
			if domain.IsMissing(err) {
				return &observationPreviewError{err}
			}
			return &attachmentPrerequisiteError{err}
		}
		if source.Kind == "message.deleted" {
			return &observationPreviewError{domain.Missing("message", messageID)}
		}
		if source.ConnectionID != connection.ID {
			return &attachmentPrerequisiteError{domain.Forbidden("forward source is outside the selected account")}
		}
		resolver = &messagePartResolver{service: s, binding: b, chat: chat, connection: connection, messageID: messageID, adapter: adapter,
			bytesLeft: budget.bytesLeft, callsLeft: budget.calls, nodesLeft: 2048, forwards: map[string]json.RawMessage{}, parentID: source.MessageID, quoted: quoted}
		return nil
	}
	var walk func([]domain.MessagePart, int) error
	walk = func(current []domain.MessagePart, depth int) error {
		for i := range current {
			part := &current[i]
			if part.Forward == nil {
				continue
			}
			forward := part.Forward
			if depth >= 2 {
				if len(forward.Messages) != 0 || forward.Total > 0 || forward.ExternalID != "" {
					forward.Status = "truncated"
				}
				forward.Messages = nil
				continue
			}
			if len(forward.Messages) == 0 && forward.ExternalID != "" && len(forward.Raw) == 0 {
				if budget.messages <= 0 || budget.calls <= 0 {
					forward.Status = "truncated"
					continue
				}
				if ctx.Err() != nil {
					forward.Status = "unavailable"
					continue
				}
				if err := resolve(); err != nil {
					var optional *observationPreviewError
					if ctx.Err() != nil || errors.As(err, &optional) {
						forward.Status = "unavailable"
						continue
					}
					return err
				}
				messages, total, err := resolver.forwardPage(ctx, *part, 0, min(napcatForwardPeek, budget.messages))
				budget.calls, budget.bytesLeft = resolver.callsLeft, resolver.bytesLeft
				if err != nil {
					forward.Status = "unavailable"
					continue
				}
				if err := resolver.authorize(ctx); err != nil {
					if ctx.Err() != nil || domain.IsMissing(err) {
						forward.Status = "unavailable"
						continue
					}
					return &attachmentPrerequisiteError{err}
				}
				forward.Messages, forward.Total, forward.Status = messages, total, "inline"
				if len(messages) < total {
					forward.Status = "truncated"
				}
			}
			kept := 0
			for j := range forward.Messages {
				if budget.messages <= 0 {
					break
				}
				budget.messages--
				kept++
				if err := walk(forward.Messages[j].Parts, depth+1); err != nil {
					return err
				}
			}
			if kept < len(forward.Messages) {
				forward.Status = "truncated"
				forward.Messages = forward.Messages[:kept]
			}
		}
		return nil
	}
	if err := walk(prepared, 0); err != nil {
		return nil, err
	}
	return prepared, nil
}

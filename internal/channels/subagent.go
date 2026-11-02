package channels

import (
	"context"
	"strings"

	"github.com/oines/astral/internal/domain"
)

// Computer API access does not let an internal Session bypass its parent and
// communicate directly with a chat. Check the persisted identity at the actual
// effect boundary, including requests made without the model's send tool.
func (s *Service) requireExternalMessaging(ctx context.Context, agentID, sessionID string) error {
	var session domain.Session
	if err := s.Store.Get(ctx, "sessions", sessionID, &session); err != nil {
		return err
	}
	if session.AgentID != agentID || session.Deletion != nil {
		return domain.Missing("Session", sessionID)
	}
	if session.SubagentParentID != "" {
		return domain.Forbidden("Subagents report to their direct parent and cannot send external messages")
	}
	return nil
}

func (s *Service) requireIMBackground(ctx context.Context, binding domain.ConversationBinding) error {
	var session domain.Session
	if err := s.Store.Get(ctx, "sessions", binding.SessionID, &session); err != nil {
		return err
	}
	if session.AgentID != binding.AgentID || session.Deletion != nil {
		return domain.Missing("Session", binding.SessionID)
	}
	if session.SubagentParentID != "" {
		return domain.Forbidden("New IM observations can only be delivered to ordinary Sessions")
	}
	return nil
}

// Named account operations also include sends, forwarding, reactions and
// notifications. Preserve platform reads while closing those alternative
// communication paths for internal Sessions.
func chatOperationReads(adapter Adapter, name string) bool {
	switch adapter.(type) {
	case *telegram:
		return strings.HasPrefix(name, "get")
	case *napcat:
		for _, spec := range napcatOperationSpecs {
			if spec.name == name {
				return !spec.mutation
			}
		}
	case *feishu:
		return name == "get_chat" || name == "get_message" || name == "list_messages" || name == "list_reactions"
	}
	return false
}

package channels

import (
	"context"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func (s *Service) activeBinding(ctx context.Context, conversationID string) (domain.ConversationBinding, error) {
	bindings, err := list[domain.ConversationBinding](ctx, s.Store, "bindings")
	if err != nil {
		return domain.ConversationBinding{}, err
	}
	for _, binding := range bindings {
		if binding.Enabled && binding.ConversationID == conversationID {
			return binding, nil
		}
	}
	return domain.ConversationBinding{}, nil
}

// Bind selects an existing unbound Session or creates one for this account's
// Agent. A manual binding starts at the current archive head, without replay.
func (s *Service) Bind(ctx context.Context, conversationID, sessionID string) (domain.ConversationBinding, error) {
	return s.bind(ctx, conversationID, sessionID, nil)
}

func (s *Service) bind(ctx context.Context, conversationID, sessionID string, firstSequence *int64) (domain.ConversationBinding, error) {
	s.bindings.Lock()
	defer s.bindings.Unlock()
	var conversation domain.Conversation
	if err := s.Store.Get(ctx, "conversations", conversationID, &conversation); err != nil {
		return domain.ConversationBinding{}, err
	}
	var connection domain.Connection
	if err := s.Store.Get(ctx, "connections", conversation.ConnectionID, &connection); err != nil {
		return domain.ConversationBinding{}, err
	}
	old, err := s.activeBinding(ctx, conversationID)
	if err != nil {
		return old, err
	}
	if old.ID != "" && (firstSequence != nil || sessionID == old.SessionID) {
		return old, nil
	}
	var agent domain.Agent
	if err = s.Store.Get(ctx, "agents", connection.AgentID, &agent); err != nil {
		return domain.ConversationBinding{}, err
	}
	var record domain.Session
	var stop []string
	if old.ID != "" {
		stop = append(stop, old.SessionID)
	}
	if sessionID != "" {
		if err = s.Store.Get(ctx, "sessions", sessionID, &record); err != nil {
			return domain.ConversationBinding{}, err
		}
		if record.AgentID != agent.ID {
			return domain.ConversationBinding{}, domain.Forbidden("Session belongs to a different Agent")
		}
		if record.BindingID != "" {
			return domain.ConversationBinding{}, domain.Conflict("Session is already bound to another chat")
		}
		if record.Deletion != nil {
			return domain.ConversationBinding{}, domain.Conflict("Session is being permanently deleted")
		}
		stop = append(stop, sessionID)
	} else {
		ref, options, selectionErr := s.Harness.Models.Selection(ctx, agent.DefaultModelID)
		if selectionErr != nil {
			return domain.ConversationBinding{}, selectionErr
		}
		if agent.DefaultReasoning != nil {
			options.Reasoning = *agent.DefaultReasoning
		}
		record = domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: agent.ID, Source: "im", ProfileID: agent.DefaultModelID, ModelRef: ref, Options: options, Reasoning: agent.DefaultReasoning, ComputerID: agent.DefaultComputerID}
	}
	binding := domain.ConversationBinding{Meta: domain.Meta{ID: domain.ID("binding")}, ConversationID: conversation.ID, AgentID: agent.ID, SessionID: record.ID, Enabled: true}
	err = s.Harness.WithStoppedSessions(ctx, stop, func() error {
		s.gate.Lock()
		defer s.gate.Unlock()
		var current domain.Connection
		if err := s.Store.Get(ctx, "connections", connection.ID, &current); err != nil {
			return err
		}
		if firstSequence != nil && (!current.Enabled || current.AccessMode != "all") {
			return domain.Conflict("automatic chat access is no longer enabled")
		}
		head, err := s.Store.ObservationHead(ctx, conversationID)
		if err != nil {
			return err
		}
		binding.StartSequence = head
		if firstSequence != nil {
			binding.StartSequence = *firstSequence - 1
		}
		return s.Store.Update(ctx, func(tx *persistence.Tx) error {
			if old.ID != "" {
				old.Enabled = false
				if err := tx.Save(ctx, "bindings", &old.Meta, &old); err != nil {
					return err
				}
				var previous domain.Session
				if err := tx.Get(ctx, "sessions", old.SessionID, &previous); err != nil {
					return err
				}
				clearSessionBinding(&previous)
				if err := tx.Save(ctx, "sessions", &previous.Meta, &previous); err != nil {
					return err
				}
			}
			if sessionID != "" {
				if err := tx.Get(ctx, "sessions", sessionID, &record); err != nil {
					return err
				}
				if record.Deletion != nil {
					return domain.Conflict("Session is being permanently deleted")
				}
			}
			record.ConversationID, record.BindingID, record.ChannelPlatform = conversationID, binding.ID, connection.Platform
			record.Title, record.TitleSource = conversation.Title, "conversation"
			if err := tx.Save(ctx, "sessions", &record.Meta, &record); err != nil {
				return err
			}
			return tx.Save(ctx, "bindings", &binding.Meta, &binding)
		})
	})
	if err == nil {
		err = s.Harness.InitializeSession(ctx, binding.SessionID)
	}
	return binding, err
}

func clearSessionBinding(session *domain.Session) {
	session.ConversationID, session.BindingID, session.ChannelPlatform = "", "", ""
	session.TitleSource = domain.TitleManual
}

func (s *Service) Unbind(ctx context.Context, id string, revision int64) error {
	s.bindings.Lock()
	defer s.bindings.Unlock()
	var binding domain.ConversationBinding
	if err := s.Store.Get(ctx, "bindings", id, &binding); err != nil {
		return err
	}
	if binding.Revision != revision {
		return domain.Conflict("binding changed; reload before unbinding")
	}
	return s.Harness.WithStoppedSessions(ctx, []string{binding.SessionID}, func() error {
		s.gate.Lock()
		defer s.gate.Unlock()
		_, _, connection, err := s.target(ctx, binding.AgentID, binding.SessionID, binding.ConversationID)
		if err != nil {
			return err
		}
		if connection.AccessMode == "all" {
			return domain.Conflict("switch this account to specified chats before removing a chat")
		}
		return s.Store.Update(ctx, func(tx *persistence.Tx) error {
			binding.Enabled = false
			if err := tx.Save(ctx, "bindings", &binding.Meta, &binding); err != nil {
				return err
			}
			var session domain.Session
			if err := tx.Get(ctx, "sessions", binding.SessionID, &session); err != nil {
				return err
			}
			clearSessionBinding(&session)
			return tx.Save(ctx, "sessions", &session.Meta, &session)
		})
	})
}

func (s *Service) target(ctx context.Context, agentID, sessionID, conversationID string) (domain.ConversationBinding, domain.Conversation, domain.Connection, error) {
	var binding domain.ConversationBinding
	var conversation domain.Conversation
	var connection domain.Connection
	if conversationID == "" {
		var session domain.Session
		if err := s.Store.Get(ctx, "sessions", sessionID, &session); err != nil {
			return binding, conversation, connection, err
		}
		if session.AgentID != agentID {
			return binding, conversation, connection, domain.Forbidden("Session is outside the current Agent view")
		}
		conversationID = session.ConversationID
		if conversationID == "" {
			return binding, conversation, connection, domain.Invalid("this Session has no default chat")
		}
	}
	if err := s.Store.Get(ctx, "conversations", conversationID, &conversation); err != nil {
		return binding, conversation, connection, err
	}
	if err := s.Store.Get(ctx, "connections", conversation.ConnectionID, &connection); err != nil {
		return binding, conversation, connection, err
	}
	if connection.AgentID != agentID {
		return binding, conversation, connection, domain.Forbidden("account is outside the current Agent view")
	}
	var err error
	binding, err = s.activeBinding(ctx, conversationID)
	if err != nil {
		return binding, conversation, connection, err
	}
	if connection.AccessMode != "all" && !binding.Enabled {
		return binding, conversation, connection, domain.Forbidden("chat is not in the account's specified list")
	}
	if !connection.Enabled {
		return binding, conversation, connection, domain.Offline("the chat account is disabled")
	}
	return binding, conversation, connection, nil
}

func (s *Service) VisibleConnections(ctx context.Context, agentID string) ([]domain.Connection, error) {
	all, err := list[domain.Connection](ctx, s.Store, "connections")
	if err != nil {
		return nil, err
	}
	out := []domain.Connection{}
	for _, c := range all {
		if c.AgentID == agentID {
			c.CredentialID, c.Config = "", nil
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Service) VisibleConversations(ctx context.Context, agentID string) ([]domain.Conversation, error) {
	all, err := list[domain.Conversation](ctx, s.Store, "conversations")
	if err != nil {
		return nil, err
	}
	out := []domain.Conversation{}
	for _, c := range all {
		if _, _, _, err := s.target(ctx, agentID, "", c.ID); err == nil {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Service) AuthorizeConversation(ctx context.Context, agentID, conversationID string) error {
	_, _, _, err := s.target(ctx, agentID, "", conversationID)
	return err
}

// ResolveForAgent permits discovery without activating a Session. In specified
// mode only an existing authorized chat can be returned, even by a new alias.
func (s *Service) ResolveForAgent(ctx context.Context, agentID, connectionID, externalID, topicID string) (domain.Conversation, error) {
	var connection domain.Connection
	if err := s.Store.Get(ctx, "connections", connectionID, &connection); err != nil {
		return domain.Conversation{}, err
	}
	if connection.AgentID != agentID {
		return domain.Conversation{}, domain.Forbidden("account is outside the current Agent view")
	}
	if !connection.Enabled {
		return domain.Conversation{}, domain.Offline("the chat account is disabled")
	}
	adapter, err := s.adapter(ctx, connection)
	if err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := adapter.ResolveConversation(ctx, externalID, topicID)
	if err != nil {
		return conversation, err
	}
	if connection.AccessMode != "all" {
		if err := s.AuthorizeConversation(ctx, agentID, conversation.ID); err != nil {
			return domain.Conversation{}, err
		}
	}
	err = s.saveConversation(ctx, &conversation)
	return conversation, err
}

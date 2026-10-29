package harness

import (
	"context"
	"encoding/json"
	"errors"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/schedule"
)

// LockAgent keeps creation, including Core Fork creation, on one side of the
// durable deletion boundary. Callers must release it before deleting a Bot.
func (s *Service) LockAgent(ctx context.Context, id string) (func(), error) {
	s.agentMu.RLock()
	if err := s.agentAvailable(ctx, id); err != nil {
		s.agentMu.RUnlock()
		return nil, err
	}
	return s.agentMu.RUnlock, nil
}

func (s *Service) agentAvailable(ctx context.Context, id string) error {
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", id, &agent); err != nil {
		return err
	}
	if agent.Deletion {
		return domain.Missing("Bot", id)
	}
	return nil
}

func availableAgent(ctx context.Context, tx *persistence.Tx, id string) error {
	var agent domain.Agent
	if err := tx.Get(ctx, "agents", id, &agent); err != nil {
		return err
	}
	if agent.Deletion {
		return domain.Missing("Bot", id)
	}
	return nil
}

// DeleteAgent records one irreversible intent before removing any owned data.
// A retry ignores the old confirmation revision only after that intent exists.
func (s *Service) DeleteAgent(ctx context.Context, id string, revision int64) error {
	s.deleteMu.Lock()
	defer s.deleteMu.Unlock()
	if _, ok := s.CoreStore.(core.SessionDeleter); !ok {
		return domain.Conflict("this history store does not support permanent Bot deletion")
	}
	s.agentMu.Lock()
	err := s.checkAgentDeletion(ctx, id, revision)
	if err == nil {
		err = s.beginAgentDeletion(ctx, id, revision)
	}
	s.agentMu.Unlock()
	if err != nil {
		return err
	}
	var agent domain.Agent
	if err = s.Store.Get(ctx, "agents", id, &agent); domain.IsMissing(err) {
		return nil
	} else if err != nil {
		return err
	}
	// IM workers include polling, Inbox, Probe, and in-flight sends. Their
	// cancellation and join must precede permanent channel-store cleanup.
	if im, ok := s.IM.(interface {
		DeleteAgentData(context.Context, string) error
	}); ok {
		if err = im.DeleteAgentData(ctx, id); err != nil {
			return err
		}
	} else {
		connections, listErr := s.Store.List(ctx, "connections")
		if listErr != nil {
			return listErr
		}
		for _, item := range connections {
			var connection domain.Connection
			if err = json.Unmarshal(item.Data, &connection); err != nil {
				return err
			}
			if connection.AgentID == id {
				return domain.Conflict("IM cleanup is unavailable; Bot deletion will resume when the channel service is available")
			}
		}
	}
	items, err := s.Store.List(ctx, "sessions")
	if err != nil {
		return err
	}
	for _, item := range items {
		var session domain.Session
		if err = json.Unmarshal(item.Data, &session); err != nil {
			return err
		}
		if session.AgentID == id {
			if err = s.deleteSession(ctx, session.ID, session.Revision, true); err != nil {
				return err
			}
		}
	}
	if err = s.Recall.DeleteAgent(ctx, id); err != nil {
		return err
	}
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var current domain.Agent
		if err := tx.Get(ctx, "agents", id, &current); err != nil {
			return err
		}
		if !current.Deletion {
			return domain.Conflict("Bot deletion intent is missing")
		}
		if err := tx.PurgeAgentData(ctx, id); err != nil {
			return err
		}
		if err := schedule.PurgeAgent(ctx, tx, id); err != nil {
			return err
		}
		if err := tx.Purge(ctx, "agents", id, current.Revision); err != nil {
			return err
		}
		var setup Onboarding
		if err := tx.Get(ctx, "onboarding", "owner", &setup); err != nil && !domain.IsMissing(err) {
			return err
		}
		if setup.AgentID == id && !setup.Completed {
			setup.Completed = true
			return tx.Save(ctx, "onboarding", &setup.Meta, &setup)
		}
		return nil
	})
}

func (s *Service) checkAgentDeletion(ctx context.Context, id string, revision int64) error {
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", id, &agent); domain.IsMissing(err) {
		return nil
	} else if err != nil {
		return err
	}
	if agent.Deletion {
		return nil
	}
	if agent.Revision != revision {
		return domain.Conflict("Bot 已发生变化，请刷新后重试。")
	}
	if err := s.checkPrivateAgentRollouts(ctx, id); err != nil {
		return err
	}
	if im, ok := s.IM.(interface {
		CheckAgentDeletion(context.Context, string) error
	}); ok {
		return im.CheckAgentDeletion(ctx, id)
	}
	return nil
}

// SessionDeleter cannot erase a Rollout that another Session still references.
// Reject known shared history before recording an irreversible Bot intent;
// otherwise every restart would resume a deletion that can never finish.
func (s *Service) checkPrivateAgentRollouts(ctx context.Context, id string) error {
	items, err := s.Store.List(ctx, "sessions")
	if err != nil {
		return err
	}
	sessions := make([]domain.Session, 0, len(items))
	for _, item := range items {
		var session domain.Session
		if err := json.Unmarshal(item.Data, &session); err != nil {
			return err
		}
		sessions = append(sessions, session)
	}
	rollouts := map[string]bool{}
	for _, session := range sessions {
		if session.AgentID != id {
			continue
		}
		state, err := s.CoreStore.LoadState(ctx, session.ID)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if rollouts[state.RolloutID] {
			return domain.Conflict("此 Bot 的会话与其他会话共享历史，暂时不能永久删除。")
		}
		rollouts[state.RolloutID] = true
	}
	if len(rollouts) == 0 {
		return nil
	}
	for _, session := range sessions {
		if session.AgentID == id {
			continue
		}
		state, err := s.CoreStore.LoadState(ctx, session.ID)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if rollouts[state.RolloutID] {
			return domain.Conflict("此 Bot 的会话与其他会话共享历史，暂时不能永久删除。")
		}
	}
	return nil
}

func (s *Service) beginAgentDeletion(ctx context.Context, id string, revision int64) error {
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var agent domain.Agent
		if err := tx.Get(ctx, "agents", id, &agent); domain.IsMissing(err) {
			return nil
		} else if err != nil {
			return err
		}
		if agent.Deletion {
			return nil
		}
		if agent.Revision != revision {
			return domain.Conflict("Bot 已发生变化，请刷新后重试。")
		}
		agent.Deletion = true
		if err := tx.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
			return err
		}
		items, err := tx.List(ctx, "sessions")
		if err != nil {
			return err
		}
		for _, item := range items {
			var session domain.Session
			if err = json.Unmarshal(item.Data, &session); err != nil {
				return err
			}
			if session.AgentID != id || session.Deletion != nil {
				continue
			}
			state, loadErr := s.CoreStore.LoadState(ctx, session.ID)
			if loadErr != nil && !errors.Is(loadErr, core.ErrNotFound) {
				return loadErr
			}
			session.Deletion = &domain.SessionDeletion{RolloutID: state.RolloutID}
			if err = tx.Save(ctx, "sessions", &session.Meta, &session); err != nil {
				return err
			}
		}
		for _, kind := range []string{"connections", "bindings"} {
			items, err = tx.List(ctx, kind)
			if err != nil {
				return err
			}
			for _, item := range items {
				if kind == "connections" {
					var connection domain.Connection
					if err = json.Unmarshal(item.Data, &connection); err != nil {
						return err
					}
					if connection.AgentID == id && connection.Enabled {
						connection.Enabled = false
						if err = tx.Save(ctx, kind, &connection.Meta, &connection); err != nil {
							return err
						}
					}
				} else {
					var binding domain.ConversationBinding
					if err = json.Unmarshal(item.Data, &binding); err != nil {
						return err
					}
					if binding.AgentID == id && binding.Enabled {
						binding.Enabled = false
						if err = tx.Save(ctx, kind, &binding.Meta, &binding); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	})
}

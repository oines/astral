package channels

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type agentWorkers struct {
	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
}

// agentWorkersLocked and all Add calls run under s.mu. Once deletion sets the
// gate, no new worker can join a group being waited on.
func (s *Service) agentWorkersLocked(id string) *agentWorkers {
	if group := s.agentWorkers[id]; group != nil {
		return group
	}
	ctx, cancel := context.WithCancel(s.ctx)
	group := &agentWorkers{ctx: ctx, cancel: cancel}
	s.agentWorkers[id] = group
	return group
}

// DeleteAgentData stops only this Bot's workers before Harness purges its
// channel records in the final Host transaction. No platform delete API is used.
func (s *Service) DeleteAgentData(ctx context.Context, id string) error {
	s.mu.Lock()
	s.deletingAgents[id] = true
	group := s.agentWorkers[id]
	if group != nil {
		group.cancel()
	}
	for key, login := range s.weixinLogins {
		if login.agentID == id {
			delete(s.weixinLogins, key)
		}
	}
	s.mu.Unlock()
	if group != nil {
		done := make(chan struct{})
		go func() { group.workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) activeAgent(ctx context.Context, id string) error {
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", id, &agent); err != nil {
		return err
	}
	if agent.Deletion {
		return domain.Missing("Bot", id)
	}
	return nil
}

// CheckAgentDeletion is a read-only preflight before Harness commits an intent.
// An old anonymous cache cannot be attributed safely to one of several Bots.
func (s *Service) CheckAgentDeletion(ctx context.Context, id string) error {
	wire, err := list[weixinWireRecord](ctx, s.Store, "weixin_wire")
	if err != nil {
		return err
	}
	unknown := false
	for _, record := range wire {
		unknown = unknown || record.ConnectionID == ""
	}
	if !unknown {
		return nil
	}
	items, err := s.Store.Retained(ctx, "connections")
	if err != nil {
		return err
	}
	for _, item := range items {
		var connection domain.Connection
		if err := json.Unmarshal(item.Data, &connection); err != nil {
			return err
		}
		if connection.AgentID == id && connection.Adapter == "weixin.ilink" {
			return domain.Conflict("旧版微信缓存缺少账号归属，请先运行 IM 数据整理，再删除此 Bot。")
		}
	}
	return nil
}

// archiveEvent serializes late platform-operation replies with the deletion
// intent, including HTTP operations that are not background IM workers.
func (s *Service) archiveEvent(ctx context.Context, event domain.Observation) (bool, error) {
	inserted := false
	err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var connection domain.Connection
		if err := tx.Get(ctx, "connections", event.ConnectionID, &connection); err != nil {
			return err
		}
		if err := tx.RequireActiveAgent(ctx, connection.AgentID); err != nil {
			return err
		}
		var conversation domain.Conversation
		if err := tx.Get(ctx, "conversations", event.ConversationID, &conversation); err != nil {
			return err
		}
		if conversation.ConnectionID != event.ConnectionID {
			return domain.Forbidden("the event belongs to a different channel account")
		}
		var err error
		inserted, err = tx.Archive(ctx, event)
		return err
	})
	return inserted, err
}

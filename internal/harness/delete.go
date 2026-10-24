package harness

import (
	"context"
	"encoding/json"
	"errors"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

// DeleteSession records an irreversible intent before touching Core or recall.
// Every cleanup phase is idempotent, so a retry or server restart can complete
// an interrupted deletion without ever reopening the execution runtime.
func (s *Service) DeleteSession(ctx context.Context, id string, revision int64) error {
	s.deleteMu.Lock()
	defer s.deleteMu.Unlock()
	deleter, ok := s.CoreStore.(core.SessionDeleter)
	if !ok {
		return domain.Conflict("this history store does not support permanent Session deletion")
	}
	var record domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &record); err != nil {
		if domain.IsMissing(err) {
			return nil
		}
		return err
	}
	if record.Deletion == nil {
		state, err := s.CoreStore.LoadState(ctx, id)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
			if err := tx.Get(ctx, "sessions", id, &record); err != nil {
				return err
			}
			if record.Revision != revision {
				return domain.Conflict("会话已发生变化，请刷新后重试。")
			}
			if !record.Archived {
				return domain.Conflict("请先归档会话，再永久删除。")
			}
			if record.BindingID != "" {
				return domain.Conflict("此会话仍绑定平台聊天，请先在 Bot 设置中更换会话。")
			}
			record.Deletion = &domain.SessionDeletion{RolloutID: state.RolloutID}
			return tx.Save(ctx, "sessions", &record.Meta, &record)
		})
		if err != nil {
			return err
		}
	}
	if err := s.closeDeletedRuntime(ctx, id); err != nil {
		return err
	}
	if err := deleter.DeleteSession(ctx, id); err != nil {
		if errors.Is(err, core.ErrConflict) {
			// Shared Rollout rejection is atomic: restore the unchanged record.
			if saveErr := s.Store.Update(ctx, func(tx *persistence.Tx) error {
				if err := tx.Get(ctx, "sessions", id, &record); err != nil {
					return err
				}
				record.Deletion = nil
				return tx.Save(ctx, "sessions", &record.Meta, &record)
			}); saveErr != nil {
				return saveErr
			}
			s.removeDeletedRuntime(id)
			return domain.Conflict("此会话与其他会话共享历史，不能单独永久删除。")
		}
		return err
	}
	if err := s.Recall.DeleteSession(ctx, id, record.Deletion.RolloutID); err != nil {
		return err
	}
	if err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var current domain.Session
		if err := tx.Get(ctx, "sessions", id, &current); err != nil {
			return err
		}
		if current.Deletion == nil {
			return domain.Conflict("Session deletion intent is missing")
		}
		return tx.Purge(ctx, "sessions", id, current.Revision)
	}); err != nil {
		return err
	}
	s.removeDeletedRuntime(id)
	s.notify(id)
	return nil
}

func (s *Service) closeDeletedRuntime(ctx context.Context, id string) error {
	s.mu.Lock()
	r := s.runtimes[id]
	s.mu.Unlock()
	if r == nil {
		return nil
	}
	select {
	case <-r.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	session := r.core.Load()
	r.mu.Unlock()
	// A running tool can need r.mu while observing cancellation. The durable
	// intent already blocks new admissions, so never hold it while joining Core.
	if session != nil {
		if err := session.Close(ctx); err != nil {
			return err
		}
	}
	if r.tools != nil {
		return r.tools.Close(ctx)
	}
	return nil
}

func (s *Service) removeDeletedRuntime(id string) {
	s.mu.Lock()
	delete(s.runtimes, id)
	s.mu.Unlock()
}

// ResumeSessionDeletions runs before serving requests or starting IM workers.
func (s *Service) ResumeSessionDeletions(ctx context.Context) error {
	items, err := s.Store.List(ctx, "sessions")
	if err != nil {
		return err
	}
	for _, item := range items {
		var record domain.Session
		if err = json.Unmarshal(item.Data, &record); err != nil {
			return err
		}
		if record.Deletion != nil {
			if err = s.DeleteSession(ctx, record.ID, record.Revision); err != nil {
				return err
			}
		}
	}
	return nil
}

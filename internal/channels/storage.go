package channels

import (
	"context"
	"fmt"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type ResetStorageRequest struct {
	SessionID string `json:"session_id"`
	Revision  int64  `json:"revision"`
}

type archiveRetention struct {
	domain.Meta
	AgentID        string    `json:"agent_id"`
	ConversationID string    `json:"conversation_id"`
	RetentionDays  int       `json:"retention_days"`
	LastRun        time.Time `json:"last_run,omitempty"`
}

func (s *Service) ResetStorageChat(ctx context.Context, conversation string, request ResetStorageRequest) (domain.ConversationBinding, error) {
	if request.SessionID == "" || request.Revision < 1 {
		return domain.ConversationBinding{}, domain.Invalid("current session_id and binding revision are required")
	}
	// Rebinding creates the replacement and archives the previous root in one
	// Host transaction. An interrupted cleanup leaves the old root accessible
	// from Storage, and a stale retry cannot reset the new binding.
	binding, err := s.changeBinding(ctx, conversation, "", nil, &request)
	if err != nil {
		return binding, err
	}
	var old domain.Session
	if err := s.Store.Get(ctx, "sessions", request.SessionID, &old); domain.IsMissing(err) {
		return binding, nil
	} else if err != nil {
		return binding, err
	}
	if err := s.Harness.DeleteSession(ctx, old.ID, old.Revision); err != nil {
		return binding, fmt.Errorf("聊天已切换到新会话，清理旧会话失败：%w", err)
	}
	return binding, nil
}

func (s *Service) storageChatOwner(ctx context.Context, conversation string) (string, error) {
	var chat domain.Conversation
	if err := s.Store.Get(ctx, "conversations", conversation, &chat); err != nil {
		return "", err
	}
	var account domain.Connection
	if err := s.Store.Get(ctx, "connections", chat.ConnectionID, &account); err != nil {
		return "", err
	}
	return account.AgentID, s.activeAgent(ctx, account.AgentID)
}

func (s *Service) ClearChatArchive(ctx context.Context, conversation string) (int, error) {
	if _, err := s.storageChatOwner(ctx, conversation); err != nil {
		return 0, err
	}
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	return s.pruneChatArchive(ctx, conversation, time.Time{})
}

func (s *Service) SetChatRetention(ctx context.Context, conversation string, days int) (int, error) {
	if days != 0 && days != 30 && days != 90 && days != 180 {
		return 0, domain.Invalid("retention must be 0, 30, 90 or 180 days")
	}
	agentID, err := s.storageChatOwner(ctx, conversation)
	if err != nil {
		return 0, err
	}
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	var policy archiveRetention
	if err := s.Store.Get(ctx, "storage_retention", conversation, &policy); err != nil && !domain.IsMissing(err) {
		return 0, err
	}
	if policy.RetentionDays == days && (days == 0 || !policy.LastRun.IsZero()) {
		return 0, nil
	}
	policy.ID, policy.AgentID, policy.ConversationID = conversation, agentID, conversation
	policy.RetentionDays, policy.LastRun = days, time.Time{}
	if err := s.saveRetention(ctx, &policy); err != nil {
		return 0, err
	}
	return s.applyRetention(ctx, &policy)
}

func (s *Service) saveRetention(ctx context.Context, policy *archiveRetention) error {
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.RequireActiveAgent(ctx, policy.AgentID); err != nil {
			return err
		}
		return tx.Save(ctx, "storage_retention", &policy.Meta, policy)
	})
}

func (s *Service) applyRetention(ctx context.Context, policy *archiveRetention) (int, error) {
	if policy.RetentionDays == 0 {
		return 0, nil
	}
	now := time.Now().UTC()
	removed, err := s.pruneChatArchive(ctx, policy.ConversationID, now.AddDate(0, 0, -policy.RetentionDays))
	if err != nil {
		return removed, err
	}
	policy.LastRun = now
	return removed, s.saveRetention(ctx, policy)
}

func (s *Service) pruneChatArchive(ctx context.Context, conversation string, before time.Time) (int, error) {
	head, err := s.Store.ObservationHead(ctx, conversation)
	if err != nil {
		return 0, err
	}
	removed := 0
	defer func() {
		if removed > 0 && s.StorageChanged != nil {
			s.StorageChanged()
		}
	}()
	// Fix the upper bound at admission. A message arriving while an older
	// batch is deleted belongs to the next cleanup.
	for after := int64(0); after < head; {
		count, next, err := s.pruneArchiveBatch(ctx, conversation, after, head, before)
		if err != nil {
			return removed, err
		}
		removed += count
		if count < 256 {
			break
		}
		after = next
	}
	return removed, nil
}

func (s *Service) pruneArchiveBatch(ctx context.Context, conversation string, after, through int64, before time.Time) (int, int64, error) {
	s.bindings.Lock()
	defer s.bindings.Unlock()
	s.gate.Lock()
	defer s.gate.Unlock()
	binding, err := s.activeBinding(ctx, conversation)
	if err != nil {
		return 0, after, err
	}
	if binding.ID != "" {
		var chat domain.Conversation
		if err := s.Store.Get(ctx, "conversations", conversation, &chat); err != nil {
			return 0, after, err
		}
		position := cursor{Meta: domain.Meta{ID: binding.ID}, Sequence: binding.StartSequence}
		if err := s.Store.Get(ctx, "inbox_cursors", binding.ID, &position); err != nil && !domain.IsMissing(err) {
			return 0, after, err
		}
		handoff, err := s.handoffPosition(ctx, binding)
		if err != nil {
			return 0, after, err
		}
		through = min(through, position.Sequence, handoff)
		if proactiveActive(chat) {
			probe, err := s.probeCursor(ctx, binding)
			if err != nil {
				return 0, after, err
			}
			through = min(through, probe.Sequence)
		}
	}
	return s.Store.PruneObservations(ctx, conversation, after, through, before)
}

// Retention runs independently of IM polling and outbox delivery.
func (s *Service) maintainRetention() {
	watch := s.Store.Watch(persistence.ChangeKey{Kind: "storage_retention"})
	defer watch.Close()
	for s.ctx.Err() == nil {
		changed := watch.Changes()
		next, err := s.pruneDueArchives(s.ctx)
		if err != nil {
			next = min(next, time.Minute)
		}
		timer := time.NewTimer(next)
		select {
		case <-s.ctx.Done():
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (s *Service) pruneDueArchives(ctx context.Context) (time.Duration, error) {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	next := 24 * time.Hour
	policies, err := list[archiveRetention](ctx, s.Store, "storage_retention")
	if err != nil {
		return next, err
	}
	var failed error
	for _, policy := range policies {
		if policy.RetentionDays == 0 {
			continue
		}
		if delay := time.Until(policy.LastRun.Add(24 * time.Hour)); delay > 0 {
			next = min(next, delay)
		} else if _, err := s.applyRetention(ctx, &policy); err != nil {
			failed = err
		}
	}
	return next, failed
}

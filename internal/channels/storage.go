package channels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const storageOperations = "storage_operations"

type StorageRequest struct {
	OperationID   string     `json:"operation_id"`
	Before        *time.Time `json:"before,omitempty"`
	RetentionDays *int       `json:"retention_days,omitempty"`
	Revision      int64      `json:"revision,omitempty"`
}

type StorageOperation struct {
	domain.Meta
	AgentID        string     `json:"agent_id"`
	Kind           string     `json:"kind"`
	State          string     `json:"state"`
	ConversationID string     `json:"conversation_id"`
	OldSessionID   string     `json:"old_session_id,omitempty"`
	NewSessionID   string     `json:"new_session_id,omitempty"`
	BindingID      string     `json:"binding_id,omitempty"`
	Phase          string     `json:"phase,omitempty"`
	Before         *time.Time `json:"before,omitempty"`
	RetentionDays  *int       `json:"retention_days,omitempty"`
	RemovedCount   int        `json:"removed_count"`
	ArchiveAfter   int64      `json:"archive_after,omitempty"`
	Error          string     `json:"error,omitempty"`
	Fingerprint    string     `json:"fingerprint"`
}

type archiveRetention struct {
	domain.Meta
	AgentID        string    `json:"agent_id"`
	ConversationID string    `json:"conversation_id"`
	RetentionDays  int       `json:"retention_days"`
	LastRun        time.Time `json:"last_run,omitempty"`
	OperationID    string    `json:"operation_id,omitempty"`
	RequestedAt    time.Time `json:"requested_at,omitempty"`
}

func lockStorage(ctx context.Context, lock *sync.Mutex) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !lock.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	if err := ctx.Err(); err != nil {
		lock.Unlock()
		return err
	}
	return nil
}

func (s *Service) StorageOperation(ctx context.Context, id string) (StorageOperation, error) {
	var op StorageOperation
	err := s.Store.Get(ctx, storageOperations, id, &op)
	return op, err
}

func (s *Service) checkStorageQueue(ctx context.Context, conversationID, kind, except string) error {
	active, err := list[StorageOperation](ctx, s.Store, storageOperations)
	if err != nil {
		return err
	}
	for _, pending := range active {
		if pending.ID != except && pending.ConversationID == conversationID && pending.Kind == kind && (pending.State == "queued" || pending.State == "running") {
			return domain.Conflict("此聊天已有同类清理任务，请先核对该任务的结果。")
		}
	}
	return nil
}

func (s *Service) StartStorageOperation(ctx context.Context, conversationID, kind string, request StorageRequest) (StorageOperation, error) {
	if len(request.OperationID) < 8 || len(request.OperationID) > 128 || strings.ContainsAny(request.OperationID, "/\\\x00") {
		return StorageOperation{}, domain.Invalid("operation_id must be a stable 8–128 character identifier")
	}
	if kind != "reset" && kind != "archive" {
		return StorageOperation{}, domain.Invalid("unknown cleanup operation")
	}
	if kind == "archive" {
		if (request.Before == nil) == (request.RetentionDays == nil) {
			return StorageOperation{}, domain.Invalid("choose a cutoff date or retention policy")
		}
		if request.RetentionDays != nil && *request.RetentionDays != 0 && *request.RetentionDays != 30 && *request.RetentionDays != 90 && *request.RetentionDays != 180 {
			return StorageOperation{}, domain.Invalid("retention must be 0, 30, 90 or 180 days")
		}
		if request.Before != nil && request.Before.IsZero() {
			return StorageOperation{}, domain.Invalid("invalid cutoff date")
		}
	}
	raw, _ := json.Marshal(struct {
		Conversation string
		Kind         string
		Request      StorageRequest
	}{conversationID, kind, request})
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	if err := lockStorage(ctx, &s.storageMu); err != nil {
		return StorageOperation{}, err
	}
	defer s.storageMu.Unlock()
	if prior, err := s.StorageOperation(ctx, request.OperationID); err == nil {
		if prior.Fingerprint != fingerprint {
			return prior, domain.Conflict("operation_id was already used for another request")
		}
		return prior, nil
	} else if !domain.IsMissing(err) {
		return prior, err
	}
	if err := s.checkStorageQueue(ctx, conversationID, kind, ""); err != nil {
		return StorageOperation{}, err
	}
	var chat domain.Conversation
	if err := s.Store.Get(ctx, "conversations", conversationID, &chat); err != nil {
		return StorageOperation{}, err
	}
	var account domain.Connection
	if err := s.Store.Get(ctx, "connections", chat.ConnectionID, &account); err != nil {
		return StorageOperation{}, err
	}
	if err := s.activeAgent(ctx, account.AgentID); err != nil {
		return StorageOperation{}, err
	}
	op := StorageOperation{Meta: domain.Meta{ID: request.OperationID}, AgentID: account.AgentID, Kind: kind, State: "queued", ConversationID: conversationID, Before: request.Before, RetentionDays: request.RetentionDays, Fingerprint: fingerprint}
	if kind == "reset" {
		binding, err := s.activeBinding(ctx, conversationID)
		if err != nil {
			return op, err
		}
		if binding.ID == "" || binding.Revision != request.Revision {
			return op, domain.Conflict("聊天绑定已改变，请刷新后重试。")
		}
		op.BindingID, op.OldSessionID = binding.ID, binding.SessionID
		op.NewSessionID = "session_cleanup_" + hex.EncodeToString(sum[:16])
	}
	if err := s.saveStorageIntent(ctx, &op); err != nil {
		return op, err
	}
	return op, nil
}

func (s *Service) RetryStorageOperation(ctx context.Context, id string) (StorageOperation, error) {
	if err := lockStorage(ctx, &s.storageMu); err != nil {
		return StorageOperation{}, err
	}
	defer s.storageMu.Unlock()
	op, err := s.StorageOperation(ctx, id)
	if err != nil {
		return op, err
	}
	if op.State != "failed" {
		return op, nil
	}
	if err = s.activeAgent(ctx, op.AgentID); err != nil {
		return op, err
	}
	if err = s.checkStorageQueue(ctx, op.ConversationID, op.Kind, op.ID); err != nil {
		return op, err
	}
	op.State, op.Error = "queued", ""
	err = s.saveStorageIntent(ctx, &op)
	return op, err
}

func (s *Service) saveStorageIntent(ctx context.Context, op *StorageOperation) error {
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.RequireActiveAgent(ctx, op.AgentID); err != nil {
			return err
		}
		return tx.Save(ctx, storageOperations, &op.Meta, op)
	})
}

// Storage jobs run in the existing service lifecycle and resume queued/running
// intents after restart. Worker admission is synchronized with Bot deletion.
func (s *Service) reconcileStorage() {
	records, err := list[StorageOperation](s.ctx, s.Store, storageOperations)
	if err != nil {
		return
	}
	for _, op := range records {
		if op.State != "queued" && op.State != "running" {
			continue
		}
		s.mu.Lock()
		if s.storageRunning == nil {
			s.storageRunning = map[string]bool{}
		}
		if s.ctx.Err() != nil || s.deletingAgents[op.AgentID] || s.storageRunning[op.ID] {
			s.mu.Unlock()
			continue
		}
		s.storageRunning[op.ID] = true
		group := s.agentWorkersLocked(op.AgentID)
		group.workers.Add(1)
		s.workers.Add(1)
		s.mu.Unlock()
		go func(op StorageOperation) {
			defer s.workers.Done()
			defer group.workers.Done()
			defer func() { s.mu.Lock(); delete(s.storageRunning, op.ID); s.mu.Unlock() }()
			s.executeStorageOperation(group.ctx, op.ID)
		}(op)
	}
	s.reconcileRetention()
}

func (s *Service) executeStorageOperation(ctx context.Context, id string) {
	// Long-running execution is separate from queue admission, so another
	// chat can enqueue work and channel reconciliation keeps making progress.
	if lockStorage(ctx, &s.storageWorkMu) != nil {
		return
	}
	defer s.storageWorkMu.Unlock()
	op, err := s.StorageOperation(ctx, id)
	if err != nil || op.State == "complete" {
		return
	}
	op.State, op.Error = "running", ""
	if err = s.Store.Save(ctx, storageOperations, &op.Meta, &op); err != nil {
		return
	}
	if op.Kind == "reset" {
		err = s.resetStorageChat(ctx, &op)
	} else {
		err = s.cleanStorageArchive(ctx, &op)
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		op.State, op.Error = "failed", err.Error()
	} else {
		op.State, op.Error = "complete", ""
	}
	if s.activeAgent(ctx, op.AgentID) == nil {
		_ = s.Store.Save(ctx, storageOperations, &op.Meta, &op)
	}
	if err == nil && s.StorageChanged != nil {
		s.StorageChanged()
	}
}

func (s *Service) resetStorageChat(ctx context.Context, op *StorageOperation) error {
	if op.Phase == "" {
		current, err := s.activeBinding(ctx, op.ConversationID)
		if err != nil {
			return err
		}
		if current.ID != op.BindingID || current.SessionID != op.OldSessionID {
			return domain.Conflict("聊天绑定已改变，已停止清理。")
		}
		var old domain.Session
		if err := s.Store.Get(ctx, "sessions", op.OldSessionID, &old); err != nil {
			return err
		}
		if old.AgentID != op.AgentID {
			return domain.Forbidden("session owner changed")
		}
		if _, err := s.Harness.CreateStorageReplacement(ctx, old, op.NewSessionID); err != nil {
			return err
		}
		op.Phase = "prepared"
		if err := s.Store.Save(ctx, storageOperations, &op.Meta, op); err != nil {
			return err
		}
	}
	binding, err := s.activeBinding(ctx, op.ConversationID)
	if err != nil {
		return err
	}
	if binding.SessionID != op.NewSessionID {
		if op.Phase != "prepared" || binding.ID != op.BindingID || binding.SessionID != op.OldSessionID {
			return domain.Conflict("聊天绑定在清理期间发生改变，旧会话尚未删除。")
		}
		if _, err = s.bindExpected(ctx, op.ConversationID, op.NewSessionID, nil, op.BindingID); err != nil {
			return err
		}
	}
	if err = s.Harness.InitializeSession(ctx, op.NewSessionID); err != nil {
		return err
	}
	op.Phase = "bound"
	if err = s.Store.Save(ctx, storageOperations, &op.Meta, op); err != nil {
		return err
	}
	var old domain.Session
	err = s.Store.Get(ctx, "sessions", op.OldSessionID, &old)
	if domain.IsMissing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if old.BindingID != "" {
		return domain.Conflict("旧会话重新绑定了聊天，已停止删除。")
	}
	if old.Deletion == nil && !old.Archived {
		old.Archived = true
		if err = s.Store.Save(ctx, "sessions", &old.Meta, &old); err != nil {
			return err
		}
	}
	return s.Harness.DeleteSession(ctx, old.ID, old.Revision)
}

func (s *Service) cleanStorageArchive(ctx context.Context, op *StorageOperation) error {
	before := op.Before
	if op.RetentionDays != nil {
		var policy archiveRetention
		err := s.Store.Get(ctx, "storage_retention", op.ConversationID, &policy)
		if err != nil && !domain.IsMissing(err) {
			return err
		}
		if policy.OperationID != op.ID && policy.RequestedAt.After(op.CreatedAt) {
			return domain.Conflict("归档保留期限已被较新的操作更改，请刷新后重新选择。")
		}
		policy.ID, policy.AgentID, policy.ConversationID = op.ConversationID, op.AgentID, op.ConversationID
		policy.RetentionDays = *op.RetentionDays
		policy.OperationID, policy.RequestedAt = op.ID, op.CreatedAt
		policy.LastRun = time.Now().UTC()
		if *op.RetentionDays > 0 && op.Before == nil {
			cutoff := time.Now().UTC().AddDate(0, 0, -*op.RetentionDays)
			op.Before = &cutoff
		}
		if err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
			if err := tx.RequireActiveAgent(ctx, op.AgentID); err != nil {
				return err
			}
			if err := tx.Save(ctx, "storage_retention", &policy.Meta, &policy); err != nil {
				return err
			}
			return tx.Save(ctx, storageOperations, &op.Meta, op)
		}); err != nil {
			return err
		}
		if *op.RetentionDays == 0 {
			return nil
		}
		before = op.Before
	}
	if before == nil {
		return domain.Invalid("cleanup cutoff is missing")
	}
	for {
		n, err := s.pruneStorageArchiveBatch(ctx, op, *before)
		if err != nil {
			return err
		}
		if n < 256 {
			return nil
		}
	}
}

// Each batch rechecks the current binding and processed watermarks. Releasing
// admission between batches lets other chats progress during a large cleanup.
func (s *Service) pruneStorageArchiveBatch(ctx context.Context, op *StorageOperation, before time.Time) (int, error) {
	s.bindings.Lock()
	defer s.bindings.Unlock()
	s.gate.Lock()
	defer s.gate.Unlock()
	through := int64(math.MaxInt64)
	binding, err := s.activeBinding(ctx, op.ConversationID)
	if err != nil {
		return 0, err
	}
	if binding.ID != "" {
		var chat domain.Conversation
		if err = s.Store.Get(ctx, "conversations", op.ConversationID, &chat); err != nil {
			return 0, err
		}
		position := cursor{Meta: domain.Meta{ID: binding.ID}, Sequence: binding.StartSequence}
		if err = s.Store.Get(ctx, "inbox_cursors", binding.ID, &position); err != nil && !domain.IsMissing(err) {
			return 0, err
		}
		s.mu.Lock()
		handoff, ok := s.handoffs[binding.ID]
		s.mu.Unlock()
		if !ok {
			handoff = binding.StartSequence
		}
		through = min(position.Sequence, handoff)
		if chat.ProactiveEnabled && chat.Kind != "private" {
			probe, err := s.probeCursor(ctx, binding)
			if err != nil {
				return 0, err
			}
			through = min(through, probe.Sequence)
		}
	}
	next := *op
	n, _, err := s.Store.PruneObservations(ctx, op.ConversationID, op.ArchiveAfter, through, before, func(tx *persistence.Tx, count int, after int64) error {
		next.RemovedCount += count
		next.ArchiveAfter = after
		if err := tx.Save(ctx, storageOperations, &next.Meta, &next); err != nil {
			return err
		}
		if count > 0 {
			return tx.MarkStorageReclaim(ctx)
		}
		return nil
	})
	if err == nil {
		*op = next
	}
	return n, err
}

func (s *Service) reconcileRetention() {
	policies, err := list[archiveRetention](s.ctx, s.Store, "storage_retention")
	if err != nil {
		return
	}
	for _, policy := range policies {
		if policy.RetentionDays <= 0 || time.Since(policy.LastRun) < 24*time.Hour {
			continue
		}
		days := policy.RetentionDays
		id := "retention_" + policy.ID + "_" + time.Now().UTC().Format("20060102")
		_, _ = s.StartStorageOperation(s.ctx, policy.ConversationID, "archive", StorageRequest{OperationID: id, RetentionDays: &days})
	}
}

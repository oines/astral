package storage

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/oines/astral/internal/domain"
)

const cleanupKind = "storage_cleanup"

type cleanupRecord struct {
	domain.Meta
	CleanupOperation
	Tokens     []string    `json:"tokens"`
	Candidates []candidate `json:"candidates"`
}

func (s *Service) Cleanup(ctx context.Context, input CleanupRequest) (CleanupOperation, error) {
	if strings.TrimSpace(input.OperationID) == "" || len(input.OperationID) > 128 || len(input.Tokens) == 0 {
		return CleanupOperation{}, domain.Invalid("a stable operation_id and selected cleanup tokens are required")
	}
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	slices.Sort(input.Tokens)
	input.Tokens = slices.Compact(input.Tokens)
	var record cleanupRecord
	err := s.config.Store.Maintenance(ctx, cleanupKind, input.OperationID, &record)
	if err == nil {
		if !slices.Equal(input.Tokens, record.Tokens) {
			return record.CleanupOperation, domain.Conflict("cleanup operation_id was already used with different selections")
		}
		return s.cleanupStatus(ctx, &record), nil
	}
	if !domain.IsMissing(err) {
		return CleanupOperation{}, err
	}
	items, err := s.candidates(ctx)
	if err != nil {
		return CleanupOperation{}, err
	}
	byToken := map[string]candidate{}
	for _, item := range items {
		byToken[item.Token] = item
	}
	record = cleanupRecord{Meta: domain.Meta{ID: input.OperationID}, CleanupOperation: CleanupOperation{OperationID: input.OperationID, State: "running", RemovedTokens: []string{}}, Tokens: input.Tokens}
	for _, token := range input.Tokens {
		item, ok := byToken[token]
		if !ok || item.Protected {
			return record.CleanupOperation, domain.Conflict("cleanup selection changed or is protected; refresh the inventory")
		}
		record.Candidates = append(record.Candidates, item)
	}
	if err = s.saveCleanup(ctx, &record); err != nil {
		return record.CleanupOperation, err
	}
	return record.CleanupOperation, s.startCleanup(record)
}
func (s *Service) cleanupStatus(ctx context.Context, record *cleanupRecord) CleanupOperation {
	if record.State == "running" && !s.activeCleanup[record.OperationID] {
		record.State, record.Error = "failed", "The Server stopped before cleanup completed. Retry to reconcile the remaining files."
		_ = s.saveCleanup(ctx, record)
	}
	return record.CleanupOperation
}
func (s *Service) CleanupOperation(ctx context.Context, id string) (CleanupOperation, error) {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	var record cleanupRecord
	if err := s.config.Store.Maintenance(ctx, cleanupKind, id, &record); err != nil {
		return CleanupOperation{}, err
	}
	return s.cleanupStatus(ctx, &record), nil
}
func (s *Service) RetryCleanup(ctx context.Context, id string) (CleanupOperation, error) {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	var record cleanupRecord
	if err := s.config.Store.Maintenance(ctx, cleanupKind, id, &record); err != nil {
		return CleanupOperation{}, err
	}
	if record.State == "complete" || s.activeCleanup[id] {
		return record.CleanupOperation, nil
	}
	record.State, record.Error = "running", ""
	if err := s.saveCleanup(ctx, &record); err != nil {
		return record.CleanupOperation, err
	}
	return record.CleanupOperation, s.startCleanup(record)
}

// Caller holds cleanupMu. The durable record is written before the worker can
// remove anything, and records each completed item before moving to the next.
func (s *Service) startCleanup(record cleanupRecord) error {
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return domain.Offline("storage maintenance is shutting down")
	}
	s.workers.Add(1)
	s.mu.Unlock()
	s.activeCleanup[record.OperationID] = true
	go func() {
		defer s.workers.Done()
		defer func() { s.cleanupMu.Lock(); delete(s.activeCleanup, record.OperationID); s.cleanupMu.Unlock() }()
		var failure error
		for index, item := range record.Candidates {
			if slices.Contains(record.RemovedTokens, item.Token) {
				continue
			}
			if failure = s.ctx.Err(); failure != nil {
				break
			}
			if item.External {
				if s.config.CleanExternal == nil {
					failure = domain.Offline("Desktop cleanup is unavailable")
					break
				}
				n, err := s.config.CleanExternal(s.ctx, record.OperationID, item.Token)
				if err != nil {
					failure = err
					break
				}
				if n < 0 {
					failure = domain.Conflict("Desktop cleanup returned an invalid removed byte count")
					break
				}
				record.RemovedBytes += n
				record.RemovedTokens = append(record.RemovedTokens, item.Token)
				if failure = s.saveCleanup(s.ctx, &record); failure != nil {
					break
				}
				continue
			}
			if item.Started && item.Kind == "upgrade_backup" {
				if failure = s.resumableBackup(s.ctx, item); failure != nil {
					break
				}
				before, _, err := inspectTree(s.ctx, item.Path)
				if errors.Is(err, os.ErrNotExist) {
					before = 0
				} else if err != nil {
					failure = err
					break
				}
				if failure = os.RemoveAll(item.Path); failure != nil {
					break
				}
				record.RemovedBytes += before
				record.RemovedTokens = append(record.RemovedTokens, item.Token)
				if failure = s.saveCleanup(s.ctx, &record); failure != nil {
					break
				}
				continue
			}
			found, err := s.validateCandidate(s.ctx, item)
			if err != nil {
				failure = err
				break
			}
			if !found {
				if _, err = os.Lstat(item.Path); !errors.Is(err, os.ErrNotExist) {
					failure = domain.Conflict("cleanup item is no longer an eligible application file")
					break
				}
				// A previous process may have removed it before recording progress.
				// Absence is safe, but no new freed bytes are claimed for this retry.
			} else {
				record.Candidates[index].Started = true
				if failure = s.saveCleanup(s.ctx, &record); failure != nil {
					break
				}
				if item.Kind == "upgrade_backup" {
					s.mu.Lock()
					s.partialBackups[item.Path] = true
					s.mu.Unlock()
				}
				if err = os.RemoveAll(item.Path); err != nil {
					failure = err
					break
				}
				record.RemovedBytes += item.Bytes
			}
			record.RemovedTokens = append(record.RemovedTokens, item.Token)
			if failure = s.saveCleanup(s.ctx, &record); failure != nil {
				break
			}
		}
		record.State = "complete"
		if failure != nil {
			record.State, record.Error = "failed", failure.Error()
		}
		// Shutdown cancellation cannot erase the last known outcome; a restart
		// reconciles a retained running record instead of reporting completion.
		if s.ctx.Err() == nil {
			_ = s.saveCleanup(s.ctx, &record)
		}
		s.RequestReclaim()
	}()
	return nil
}

func (s *Service) saveCleanup(ctx context.Context, record *cleanupRecord) error {
	return s.config.Store.SaveMaintenance(ctx, cleanupKind, record.OperationID, record.State, record)
}

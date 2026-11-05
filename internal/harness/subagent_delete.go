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

func (s *Service) deleteSubagent(ctx context.Context, parent, id string) error {
	if err := lockSubagentContext(ctx, &s.deleteMu); err != nil {
		return err
	}
	defer s.deleteMu.Unlock()
	if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
		return err
	}
	defer s.subagentMu.Unlock()
	if _, err := s.subagentRecord(ctx, parent, id); err != nil {
		if domain.IsMissing(err) {
			return nil
		}
		return err
	}
	return s.deleteSubagentTree(ctx, id, 0, false)
}

// The Host tree crosses Rollouts. Persist its full membership before deleting
// any branch; Core deliberately knows nothing about delegated Session ownership.
// The caller holds deleteMu and subagentMu, including when selecting an
// initially childless root, so a concurrent Spawn cannot escape deletion.
func (s *Service) deleteSubagentTree(ctx context.Context, id string, revision int64, deletingAgent bool) error {
	deleter, ok := s.CoreStore.(core.BranchDeleter)
	if !ok {
		return domain.Conflict("this history store does not support recursive branch deletion")
	}
	var root domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &root); domain.IsMissing(err) {
		return nil
	} else if err != nil {
		return err
	}
	if root.Deletion == nil && !deletingAgent && root.SubagentParentID == "" {
		if root.Revision != revision {
			return domain.Conflict("会话已发生变化，请刷新后重试。")
		}
		if !root.Archived {
			return domain.Conflict("请先归档会话，再永久删除。")
		}
		if root.BindingID != "" {
			return domain.Conflict("此会话仍绑定平台聊天，请先在 Bot 设置中更换会话。")
		}
	}
	rows, err := s.Store.List(ctx, "sessions")
	if err != nil {
		return err
	}
	all := map[string]domain.Session{}
	for _, row := range rows {
		var record domain.Session
		if err = json.Unmarshal(row.Data, &record); err != nil {
			return err
		}
		all[record.ID] = record
	}
	selected := map[string]bool{id: true}
	order := []string{}
	var visit func(string) error
	visit = func(parent string) error {
		for child, record := range all {
			if record.SubagentParentID != parent {
				continue
			}
			if selected[child] {
				return domain.Conflict("cyclic subagent relationship")
			}
			if record.AgentID != root.AgentID {
				return domain.Conflict("subagent belongs to another Bot")
			}
			selected[child] = true
			if err := visit(child); err != nil {
				return err
			}
		}
		order = append(order, parent)
		return nil
	}
	if err = visit(id); err != nil {
		return err
	}
	states := map[string]core.RuntimeState{}
	rollouts := map[string]bool{}
	for target := range selected {
		state, loadErr := s.CoreStore.LoadState(ctx, target)
		if loadErr != nil && !errors.Is(loadErr, core.ErrNotFound) {
			return loadErr
		}
		states[target] = state
		if state.RolloutID != "" {
			rollouts[state.RolloutID] = true
		}
	}
	// A normal, unrelated shared-history Session is not silently included merely
	// because it points at a node written by this tree.
	if root.Deletion == nil {
		for other, record := range all {
			if selected[other] || record.Deletion != nil {
				continue
			}
			state, loadErr := s.CoreStore.LoadState(ctx, other)
			if errors.Is(loadErr, core.ErrNotFound) {
				continue
			}
			if loadErr != nil {
				return loadErr
			}
			if !rollouts[state.RolloutID] || state.Branch.HeadID == "" {
				continue
			}
			cursor := ""
			for {
				page, readErr := s.CoreStore.Scan(ctx, state.RolloutID, core.Query{PathHead: state.Branch.HeadID, Cursor: cursor, Limit: core.DefaultPageSize, MaxBytes: 128 << 20})
				if readErr != nil {
					return readErr
				}
				for _, entry := range page.Records {
					if selected[entry.Entry.SessionID] {
						return domain.Conflict("another Session still uses this subagent tree's history")
					}
				}
				if page.Next == "" {
					break
				}
				cursor = page.Next
			}
		}
	}
	err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
		for target := range selected {
			var record domain.Session
			if err := tx.Get(ctx, "sessions", target, &record); err != nil {
				return err
			}
			if record.Deletion != nil {
				continue
			}
			if target == id && record.SubagentParentID == "" && !deletingAgent && record.Revision != revision {
				return domain.Conflict("会话已发生变化，请刷新后重试。")
			}
			record.Deletion = &domain.SessionDeletion{RolloutID: states[target].RolloutID, TreeRootID: id}
			if err := tx.Save(ctx, "sessions", &record.Meta, &record); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Join every runtime first: one Core branch deletion can erase several
	// execution cursors in the shared tree.
	for _, target := range order {
		if err = s.closeDeletedRuntime(ctx, target); err != nil {
			return err
		}
	}
	for _, target := range order {
		var record domain.Session
		if err = s.Store.Get(ctx, "sessions", target, &record); domain.IsMissing(err) {
			continue
		} else if err != nil {
			return err
		}
		if state, err := s.CoreStore.LoadState(ctx, target); err == nil {
			// Close may just have written a cancellation. Retain that fact in
			// the surviving parent's transcript before deleting the source.
			for !selected[record.SubagentParentID] && record.SubagentParentID != "" {
				more, err := s.recordSubagentActivities(ctx, record, state)
				if err != nil {
					return err
				}
				if !more {
					break
				}
			}
		} else if !errors.Is(err, core.ErrNotFound) {
			return err
		}
		result, deleteErr := deleter.DeleteBranch(ctx, core.BranchDeleteRequest{SessionID: target})
		if deleteErr != nil {
			return deleteErr
		}
		for _, deleted := range result.SessionIDs {
			if !selected[deleted] {
				var unexpected domain.Session
				if err := s.Store.Get(ctx, "sessions", deleted, &unexpected); domain.IsMissing(err) {
					continue
				} else if err != nil {
					return err
				}
				return domain.Conflict("branch deletion included a Session outside the selected subagent tree")
			}
		}
		if err = s.Recall.DeleteSession(ctx, target, record.Deletion.RolloutID); err != nil {
			return err
		}
		err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
			var current domain.Session
			if err := tx.Get(ctx, "sessions", target, &current); err != nil {
				return err
			}
			if err := tx.PurgeSessionDeliveries(ctx, target); err != nil {
				return err
			}
			if err := schedule.PurgeSession(ctx, tx, target); err != nil {
				return err
			}
			var settings subagentSettings
			if err := tx.Get(ctx, "subagent_settings", target, &settings); err == nil {
				if err = tx.Purge(ctx, "subagent_settings", target, settings.Revision); err != nil {
					return err
				}
			} else if !domain.IsMissing(err) {
				return err
			}
			if err := tx.DeleteMaintenance(ctx, "subagent_delivery", target); err != nil {
				return err
			}
			if err := tx.DeleteMaintenance(ctx, "subagent_activation", target); err != nil {
				return err
			}
			if err := tx.DeleteMaintenance(ctx, subagentActivityCursorKind, target); err != nil {
				return err
			}
			if err := tx.PurgeSubagentActivities(ctx, target); err != nil {
				return err
			}
			if err := tx.DeleteMaintenance(ctx, subagentModelChangeKind, target); err != nil {
				return err
			}
			if err := tx.Purge(ctx, "sessions", target, current.Revision); err != nil {
				return err
			}
			return tx.MarkStorageReclaim(ctx)
		})
		if err != nil {
			return err
		}
		s.removeDeletedRuntime(target)
		s.notify(target)
	}
	if s.StorageChanged != nil {
		s.StorageChanged()
	}
	return nil
}

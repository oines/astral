package harness

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/schedule"
	"github.com/oines/astral/internal/wake"
)

const archiveCleanupKind = "session_archive"

type archiveCleanup struct {
	AgentID string   `json:"agent_id"`
	RootID  string   `json:"root_id"`
	IDs     []string `json:"ids"`
}

func (s *Service) requireRunnable(ctx context.Context, id string) error {
	var record domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &record); err != nil {
		return err
	}
	return persistence.CheckSessionRunnable(ctx, record, s.Store)
}

func (r *runtime) requireRunnable(ctx context.Context) error {
	return r.service.requireRunnable(ctx, r.id)
}

// The caller holds subagentMu, so membership cannot change through Spawn or
// deletion. No Core history is opened to collect the Host-owned tree.
func (s *Service) archiveTree(ctx context.Context, root domain.Session) ([]string, error) {
	rows, err := s.Store.List(ctx, "sessions")
	if err != nil {
		return nil, err
	}
	children := map[string][]string{}
	for _, row := range rows {
		var record domain.Session
		if err := json.Unmarshal(row.Data, &record); err != nil {
			return nil, err
		}
		if record.AgentID == root.AgentID && record.Deletion == nil && record.SubagentParentID != "" {
			children[record.SubagentParentID] = append(children[record.SubagentParentID], record.ID)
		}
	}
	ids := []string{}
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if seen[id] {
			return domain.Conflict("cyclic subagent relationship")
		}
		seen[id] = true
		ids = append(ids, id)
		for _, child := range children[id] {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root.ID); err != nil {
		return nil, err
	}
	return ids, nil
}

// Admission uses runtime.mu. Lock all existing members, then hold the runtime
// registration gate only across the Host transaction. Recheck only this tree:
// unrelated sessions opening cannot force retries or delay its archive.
func (s *Service) lockArchiveAdmissions(ctx context.Context, ids []string) (func(), error) {
	return s.lockRuntimeAdmissions(ctx, ids, true)
}

// interrupt is used only by Session sealing, whose in-flight maintenance may
// need cancellation to release admission. Resource cleanup must leave ordinary
// Runs untouched while it serializes only the corresponding delivery boundary.
func (s *Service) lockRuntimeAdmissions(ctx context.Context, ids []string, interrupt bool) (func(), error) {
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	for {
		s.mu.Lock()
		known := make(map[string]*runtime, len(ids))
		for _, id := range ordered {
			known[id] = s.runtimes[id]
		}
		s.mu.Unlock()
		locked := []*runtime{}
		unlock := func() {
			for i := len(locked) - 1; i >= 0; i-- {
				locked[i].mu.Unlock()
			}
		}
		for _, id := range ordered {
			r := known[id]
			if r == nil {
				continue
			}
			select {
			case <-r.ready:
			case <-ctx.Done():
				unlock()
				return nil, ctx.Err()
			}
			// A maintenance request can hold admission while awaiting a model.
			// Cancellation releases that lock; the sealed commit rechecks it.
			if interrupt && r.core.Load() != nil {
				r.core.Load().Cancel()
			}
			if err := lockSubagentContext(ctx, &r.mu); err != nil {
				unlock()
				return nil, err
			}
			locked = append(locked, r)
		}
		s.mu.Lock()
		stable := true
		for _, id := range ordered {
			stable = stable && s.runtimes[id] == known[id]
		}
		if stable {
			return func() { s.mu.Unlock(); unlock() }, nil
		}
		s.mu.Unlock()
		unlock()
	}
}

func (s *Service) archiveSession(ctx context.Context, agentID, id string, title *string, pinned, archived, private *bool) error {
	if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
		return err
	}
	defer s.subagentMu.Unlock()
	var root domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &root); err != nil {
		return err
	}
	if agentID != "" && root.AgentID != agentID {
		return domain.Forbidden("Session belongs to another Bot")
	}
	if err := persistence.CheckSessionAccess(ctx, root); err != nil {
		return err
	}
	if root.SubagentParentID != "" {
		return domain.Forbidden("subagents are managed through agent_control")
	}
	if root.BindingID != "" {
		return domain.Conflict("a bound Session cannot be archived")
	}
	if title != nil && strings.TrimSpace(*title) == "" {
		return domain.Invalid("title cannot be empty")
	}
	if !*archived && !root.Archived {
		return s.saveSessionPresentation(ctx, agentID, id, title, pinned, nil, private)
	}
	ids, err := s.archiveTree(ctx, root)
	if err != nil {
		return err
	}
	unlock, err := s.lockArchiveAdmissions(ctx, ids)
	if err != nil {
		return err
	}
	// Both archive and restore first establish a sealed tree. In particular,
	// restoring an old archived root must not expose its still-live children.
	err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
		for _, target := range ids {
			var record domain.Session
			if err := tx.Get(ctx, "sessions", target, &record); err != nil {
				return err
			}
			record.Archived = true
			if target == id {
				if title != nil {
					record.Title, record.TitleSource = *title, domain.TitleManual
				}
				if pinned != nil {
					record.Pinned = *pinned
				}
			}
			if private != nil {
				record.Private = *private
			}
			if err := tx.Save(ctx, "sessions", &record.Meta, &record); err != nil {
				return err
			}
		}
		if err := schedule.PauseSessions(ctx, tx, ids); err != nil {
			return err
		}
		if err := wake.PauseSessions(ctx, tx, ids); err != nil {
			return err
		}
		if err := retireArchivedProcessHandles(ctx, tx, ids); err != nil {
			return err
		}
		return tx.SaveMaintenance(ctx, archiveCleanupKind, id, "pending", archiveCleanup{AgentID: root.AgentID, RootID: id, IDs: ids})
	})
	unlock()
	if err != nil {
		return err
	}
	if s.Schedules != nil {
		s.Schedules.NotifyChanged()
	}
	if s.WakeEndpoints != nil {
		s.WakeEndpoints.InvalidateSessions(ids)
	}
	for _, target := range ids {
		s.notify(target)
	}
	if err := s.finishArchive(ctx, archiveCleanup{AgentID: root.AgentID, RootID: id, IDs: ids}); err != nil {
		return err
	}
	if !*archived {
		// Cleanup precedes the restore commit. Disabled plans/endpoints are left
		// disabled; no Continue or old task delivery is performed here.
		unlock, err := s.lockArchiveAdmissions(ctx, ids)
		if err != nil {
			return err
		}
		err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
			for _, target := range ids {
				var record domain.Session
				if err := tx.Get(ctx, "sessions", target, &record); err != nil {
					return err
				}
				record.Archived = false
				if err := tx.Save(ctx, "sessions", &record.Meta, &record); err != nil {
					return err
				}
			}
			return nil
		})
		unlock()
		if err != nil {
			return err
		}
	}
	for _, target := range ids {
		s.notify(target)
	}
	return nil
}

func (s *Service) finishArchive(ctx context.Context, intent archiveCleanup) error {
	var root domain.Session
	if err := s.Store.Get(ctx, "sessions", intent.RootID, &root); domain.IsMissing(err) {
		return s.Store.DeleteMaintenance(ctx, archiveCleanupKind, intent.RootID)
	} else if err != nil {
		return err
	}
	if root.Deletion != nil || !root.Archived || intent.AgentID != "" && intent.AgentID != root.AgentID {
		return s.Store.DeleteMaintenance(ctx, archiveCleanupKind, intent.RootID)
	}
	// Signal every loaded member before joining any of them. Running tools may
	// need admission to observe cancellation, so joining never holds runtime.mu.
	for _, id := range intent.IDs {
		s.mu.Lock()
		r := s.runtimes[id]
		s.mu.Unlock()
		if r != nil && r.core.Load() != nil {
			r.core.Load().Cancel()
		}
	}
	for _, id := range intent.IDs {
		if err := s.finishArchivedSession(ctx, id); err != nil {
			return err
		}
	}
	return s.Store.DeleteMaintenance(ctx, archiveCleanupKind, intent.RootID)
}

func (s *Service) finishArchivedSession(ctx context.Context, id string) error {
	var record domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &record); domain.IsMissing(err) {
		return nil
	} else if err != nil {
		return err
	}
	if record.Deletion != nil {
		return nil
	}
	if err := s.cancelModelFallback(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	r := s.runtimes[id]
	s.mu.Unlock()
	state, err := s.CoreStore.LoadState(ctx, id)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	// Cold history with no driving queue does not need a Core execution window.
	if r == nil && err == nil && state.PendingInputs[core.InputSteer]+state.PendingInputs[core.InputFollowUp] > 0 {
		r, err = s.open(ctx, id)
		if err != nil {
			return err
		}
	}
	if r != nil {
		select {
		case <-r.ready:
		case <-ctx.Done():
			return ctx.Err()
		}
		if r.openErr != nil {
			return r.openErr
		}
		r.retire()
		r.mu.Lock()
		r.snapshotMu.RLock()
		alreadyClosed := r.corePhase == runtimeCoreClosed
		r.snapshotMu.RUnlock()
		var clearErr error
		if !alreadyClosed {
			clearErr = r.core.Load().ClearQueuedInputs(ctx)
		}
		titleCancel, titleDone := r.titleCancel, r.titleDone
		r.mu.Unlock()
		if clearErr != nil {
			return clearErr
		}
		if titleCancel != nil {
			titleCancel()
			select {
			case <-titleDone:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err := r.closeCore(ctx); err != nil {
			return err
		}
		if r.tools != nil {
			if err := r.tools.Close(ctx); err != nil {
				return err
			}
		}
		s.removeRuntime(id)
		state, err = s.CoreStore.LoadState(ctx, id)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
	}
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		for _, kind := range []string{imReplyWatchKind, "subagent_activation", subagentModelChangeKind} {
			if err := tx.DeleteMaintenance(ctx, kind, id); err != nil {
				return err
			}
		}
		if record.SubagentParentID == "" {
			return nil
		}
		var settings subagentSettings
		if err := tx.Get(ctx, "subagent_settings", id, &settings); err != nil {
			return err
		}
		settings.Started = true
		if state.LastRun != nil {
			settings.ArchiveStoppedRunID = state.LastRun.ID
		}
		if err := tx.Save(ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
			return err
		}
		var checkpoint subagentCheckpoint
		if err := tx.Maintenance(ctx, "subagent_delivery", id, &checkpoint); err != nil && !domain.IsMissing(err) {
			return err
		}
		checkpoint.Protocol, checkpoint.Version = subagentDeliveryProtocol, state.Version
		checkpoint.SkipThroughVersion = state.Version
		return tx.SaveMaintenance(ctx, "subagent_delivery", id, "complete", checkpoint)
	})
}

func retireArchivedProcessHandles(ctx context.Context, tx *persistence.Tx, ids []string) error {
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	rows, err := tx.List(ctx, "process_handles")
	if err != nil {
		return err
	}
	for _, row := range rows {
		var handle processHandle
		if err := json.Unmarshal(row.Data, &handle); err != nil {
			return err
		}
		if !selected[handle.SessionID] || handle.Retired {
			continue
		}
		handle.Retired = true
		if err := tx.Save(ctx, "process_handles", &handle.Meta, &handle); err != nil {
			return err
		}
	}
	return nil
}

// ResumeSessionArchives completes only explicitly interrupted archive work.
// Other old archived histories stay cold and are cleaned before their restore.
func (s *Service) ResumeSessionArchives(ctx context.Context) error {
	if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
		return err
	}
	defer s.subagentMu.Unlock()
	rows, err := s.Store.PendingMaintenance(ctx, archiveCleanupKind)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var intent archiveCleanup
		if err := json.Unmarshal(row, &intent); err != nil {
			return err
		}
		if err := s.finishArchive(ctx, intent); err != nil {
			return err
		}
	}
	return nil
}

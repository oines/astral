package harness

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const subagentModelChangeKind = "subagent_model_change"

type subagentModelChange struct {
	RootID     string              `json:"root_id"`
	Selection  core.ModelSelection `json:"selection"`
	SessionIDs []string            `json:"session_ids"`
}

// A parent controls one direct child. Composer uses selectSubagentModels for
// its separate, explicit operation over the complete delegation tree.
func (s *Service) selectDirectSubagentModel(ctx context.Context, parent, id string, selection core.ModelSelection) error {
	if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
		return err
	}
	defer s.subagentMu.Unlock()
	if err := s.requireRunnable(ctx, parent); err != nil {
		return err
	}
	if err := s.requireRunnable(ctx, id); err != nil {
		return err
	}
	if _, err := s.Models.ResolveSelection(ctx, selection); err != nil {
		return err
	}
	if err := s.reconcileSubagentModelChangesLocked(ctx); err != nil {
		return err
	}
	if _, err := s.subagentRecord(ctx, parent, id); err != nil {
		return err
	}
	if err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var settings subagentSettings
		if err := tx.Get(ctx, "subagent_settings", id, &settings); err != nil {
			return err
		}
		settings.Configuration.Selection = selection
		return tx.Save(ctx, "subagent_settings", &settings.Meta, &settings)
	}); err != nil {
		return err
	}
	if err := s.setPreferredModel(ctx, id, selection); err != nil {
		return err
	}
	s.notify(id)
	s.wakeSubagents(id)
	s.mu.Lock()
	child := s.runtimes[id]
	s.mu.Unlock()
	if child == nil {
		return nil
	}
	select {
	case <-child.ready:
		if child.openErr != nil {
			return nil
		}
	default:
		return nil
	}
	child.mu.Lock()
	err := child.syncSubagentModel(ctx)
	child.mu.Unlock()
	if errors.Is(err, core.ErrBusy) {
		return nil
	}
	return err
}

func (s *Service) selectSubagentModels(ctx context.Context, id string, selection core.ModelSelection) error {
	if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
		return err
	}
	defer s.subagentMu.Unlock()
	if err := s.requireRunnable(ctx, id); err != nil {
		return err
	}
	if _, err := s.Models.ResolveSelection(ctx, selection); err != nil {
		return err
	}
	if err := s.reconcileSubagentModelChangesLocked(ctx); err != nil {
		return err
	}
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	var root domain.Session
	if err = s.Store.Get(ctx, "sessions", id, &root); err != nil {
		return err
	}
	rows, err := s.Store.List(ctx, "sessions")
	if err != nil {
		return err
	}
	selected := map[string]bool{id: true}
	change := subagentModelChange{RootID: id, Selection: selection, SessionIDs: []string{id}}
	for added := true; added; {
		added = false
		for _, row := range rows {
			var child domain.Session
			if err = json.Unmarshal(row.Data, &child); err != nil {
				return err
			}
			if child.AgentID == root.AgentID && child.Deletion == nil && child.SubagentParentID != "" && selected[child.SubagentParentID] && !selected[child.ID] {
				selected[child.ID], added = true, true
				change.SessionIDs = append(change.SessionIDs, child.ID)
			}
		}
	}
	r.mu.Lock()
	if _, err = r.readableCore(); err != nil {
		r.mu.Unlock()
		return err
	}
	previous, err := s.modelPreferredSelection(ctx, id, r.core.Load().Configuration().Selection)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	changed := !jsonEqual(previous, selection) || !jsonEqual(r.core.Load().Configuration().Selection, selection)

	// Record the intent before the first Core write. Recovery only propagates
	// it if the root's durable selection confirms that write succeeded.
	if err = s.Store.SaveMaintenance(ctx, subagentModelChangeKind, id, "running", change); err == nil {
		err = r.core.Load().SelectModel(ctx, selection)
		if errors.Is(err, core.ErrBusy) || errors.Is(err, core.ErrClosed) || errors.Is(err, core.ErrInvalid) {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err = errors.Join(err, s.Store.DeleteMaintenance(cleanup, subagentModelChangeKind, id))
			cancel()
		}
	}
	r.mu.Unlock()
	if err != nil {
		s.wakeSubagents(id) // Uncertain Core writes retain their recovery intent.
		return err
	}
	s.notify(id)
	s.wakeSubagents(id)
	if err = s.persistSubagentModelChange(ctx, change); err != nil {
		return err
	}
	for _, childID := range change.SessionIDs {
		if childID == id {
			continue
		}
		s.mu.Lock()
		child := s.runtimes[childID]
		s.mu.Unlock()
		if child == nil {
			continue // An unopened child uses its updated saved configuration.
		}
		select {
		case <-child.ready:
			if child.openErr != nil {
				continue
			}
		default:
			continue
		}
		child.mu.Lock()
		err = child.syncSubagentModel(ctx)
		child.mu.Unlock()
		if err != nil && !errors.Is(err, core.ErrBusy) {
			return err
		}
	}
	if changed && root.SubagentParentID == "" {
		return s.resumeIMSession(ctx, id)
	}
	return nil
}

// The caller holds subagentMu. Desired child selections and acknowledgement
// share one Host transaction; a crash cannot leave an untracked partial tree.
func (s *Service) persistSubagentModelChange(ctx context.Context, change subagentModelChange) error {
	for _, id := range change.SessionIDs {
		var record domain.Session
		if err := s.Store.Get(ctx, "sessions", id, &record); domain.IsMissing(err) {
			continue
		} else if err != nil {
			return err
		}
		if record.Deletion == nil {
			if err := s.setPreferredModel(ctx, id, change.Selection); err != nil {
				return err
			}
		}
	}
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		for _, id := range change.SessionIDs {
			var record domain.Session
			if err := tx.Get(ctx, "sessions", id, &record); domain.IsMissing(err) {
				continue
			} else if err != nil {
				return err
			}
			if record.Deletion != nil || record.SubagentParentID == "" {
				continue
			}
			var settings subagentSettings
			if err := tx.Get(ctx, "subagent_settings", id, &settings); err != nil {
				return err
			}
			settings.Configuration.Selection = change.Selection
			if err := tx.Save(ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
				return err
			}
		}
		return tx.DeleteMaintenance(ctx, subagentModelChangeKind, change.RootID)
	})
}

func (s *Service) reconcileSubagentModelChanges(ctx context.Context) error {
	if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
		return err
	}
	defer s.subagentMu.Unlock()
	return s.reconcileSubagentModelChangesLocked(ctx)
}

func (s *Service) reconcileSubagentModelChangesLocked(ctx context.Context) error {
	if err := s.reconcileAgentModelPriorityChangesLocked(ctx); err != nil {
		return err
	}
	changes, err := s.Store.PendingMaintenance(ctx, subagentModelChangeKind)
	if err != nil {
		return err
	}
	for _, raw := range changes {
		var change subagentModelChange
		if err = json.Unmarshal(raw, &change); err != nil {
			return err
		}
		var root domain.Session
		err = s.Store.Get(ctx, "sessions", change.RootID, &root)
		if err != nil && !domain.IsMissing(err) {
			return err
		}
		if err == nil && root.Deletion == nil {
			if gateErr := persistence.CheckSessionRunnable(ctx, root, s.Store); persistence.IsSessionArchived(gateErr) {
				if err := s.Store.DeleteMaintenance(ctx, subagentModelChangeKind, change.RootID); err != nil {
					return err
				}
				continue
			} else if gateErr != nil {
				return gateErr
			}
			configuration, readErr := core.ReadConfiguration(ctx, s.CoreStore, root.ID)
			if readErr != nil && !errors.Is(readErr, core.ErrNotFound) {
				return readErr
			}
			if readErr == nil && jsonEqual(configuration.Selection, change.Selection) {
				if err = s.persistSubagentModelChange(ctx, change); err != nil {
					return err
				}
				continue
			}
		}
		if err = s.Store.DeleteMaintenance(ctx, subagentModelChangeKind, change.RootID); err != nil {
			return err
		}
	}
	return nil
}

// Called with the tree or runtime lock. Selection is independent from the shared
// root prefix, which is resolved separately at the next safe request boundary.
func (r *runtime) syncSubagentModel(ctx context.Context) error {
	settings, err := r.service.childMetadata(ctx, r.id)
	if err != nil {
		return err
	}
	var desired core.ModelSelection
	if err := json.Unmarshal(settings.selection, &desired); err != nil {
		return err
	}
	selection, err := r.service.modelFallbackSubagentSelection(ctx, r.id, desired)
	if err != nil {
		return err
	}
	current, err := r.service.selectionAtState(ctx, r.id, r.core.Load().State())
	if err != nil {
		return err
	}
	if current.fingerprint == digest(selection) {
		return nil
	}

	if err := r.core.Load().SelectModel(ctx, selection); err != nil {
		return err
	}
	r.service.notify(r.id)
	return nil
}

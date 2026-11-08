package harness

import (
	"context"
	"encoding/json"
	"errors"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

// AgentModelPriority also reads Bots saved before ordered model selection was
// introduced. The first entry remains projected into the legacy default fields
// for callers that have not yet adopted model_priority.
func AgentModelPriority(agent domain.Agent) []core.ModelSelection {
	if len(agent.ModelPriority) > 0 {
		return append([]core.ModelSelection(nil), agent.ModelPriority...)
	}
	if agent.DefaultModelID != "" {
		return []core.ModelSelection{{ID: agent.DefaultModelID, Reasoning: agent.DefaultReasoning}}
	}
	return []core.ModelSelection{}
}

const agentModelPriorityChangeKind = "agent_model_priority"

type agentModelPriorityChange struct {
	AgentID    string              `json:"agent_id"`
	Selection  core.ModelSelection `json:"selection"`
	SessionIDs []string            `json:"session_ids"`
	Cursor     int                 `json:"cursor"`
}

// ApplyAgentModelPriority replaces individual preferences for existing Sessions
// without starting a Run. The absolute intent and cursor make interrupted or
// uncertain applications safe to reconcile and retry.
func (s *Service) ApplyAgentModelPriority(ctx context.Context, id string) (int, error) {
	if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
		return 0, err
	}
	defer s.subagentMu.Unlock()
	if err := s.reconcileSubagentModelChangesLocked(ctx); err != nil {
		return 0, err
	}
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", id, &agent); err != nil {
		return 0, err
	}
	if agent.Deletion {
		return 0, domain.Missing("Bot", id)
	}
	priority := AgentModelPriority(agent)
	if len(priority) == 0 {
		return 0, domain.Invalid("choose a model before applying it to all Sessions")
	}
	rows, err := s.Store.List(ctx, "sessions")
	if err != nil {
		return 0, err
	}
	change := agentModelPriorityChange{AgentID: id, Selection: priority[0], SessionIDs: []string{}}
	for _, row := range rows {
		var session domain.Session
		if err = json.Unmarshal(row.Data, &session); err != nil {
			return 0, err
		}
		if session.AgentID == id && session.Deletion == nil {
			if err := persistence.CheckSessionRunnable(ctx, session, s.Store); persistence.IsSessionArchived(err) {
				continue
			} else if err != nil {
				return 0, err
			}
			change.SessionIDs = append(change.SessionIDs, session.ID)
		}
	}
	if err = s.Store.SaveMaintenance(ctx, agentModelPriorityChangeKind, id, "running", change); err != nil {
		return 0, err
	}
	s.wakeSubagents()
	if err = s.applyAgentModelPriorityChangeLocked(ctx, &change); err != nil {
		return change.Cursor, err
	}
	return len(change.SessionIDs), nil
}

// Caller holds subagentMu, shared with Composer and agent_control changes.
func (s *Service) reconcileAgentModelPriorityChangesLocked(ctx context.Context) error {
	changes, err := s.Store.PendingMaintenance(ctx, agentModelPriorityChangeKind)
	if err != nil {
		return err
	}
	for _, raw := range changes {
		var change agentModelPriorityChange
		if err = json.Unmarshal(raw, &change); err != nil {
			return err
		}
		if err = s.applyAgentModelPriorityChangeLocked(ctx, &change); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyAgentModelPriorityChangeLocked(ctx context.Context, change *agentModelPriorityChange) error {
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", change.AgentID, &agent); domain.IsMissing(err) || err == nil && agent.Deletion {
		return s.Store.DeleteMaintenance(ctx, agentModelPriorityChangeKind, change.AgentID)
	} else if err != nil {
		return err
	}
	for change.Cursor < len(change.SessionIDs) {
		id := change.SessionIDs[change.Cursor]
		var record domain.Session
		err := s.Store.Get(ctx, "sessions", id, &record)
		if err != nil && !domain.IsMissing(err) {
			return err
		}
		if err == nil && record.AgentID == change.AgentID && record.Deletion == nil {
			if gateErr := persistence.CheckSessionRunnable(ctx, record, s.Store); persistence.IsSessionArchived(gateErr) {
				change.Cursor++
				if err := s.Store.SaveMaintenance(ctx, agentModelPriorityChangeKind, change.AgentID, "running", change); err != nil {
					return err
				}
				continue
			} else if gateErr != nil {
				return gateErr
			}
			if err = s.setPreferredModel(ctx, id, change.Selection); err != nil {
				return err
			}
			if record.SubagentParentID != "" {
				if err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
					var settings subagentSettings
					if err := tx.Get(ctx, "subagent_settings", id, &settings); err != nil {
						return err
					}
					if jsonEqual(settings.Configuration.Selection, change.Selection) {
						return nil
					}
					settings.Configuration.Selection = change.Selection
					return tx.Save(ctx, "subagent_settings", &settings.Meta, &settings)
				}); err != nil {
					return err
				}
			}
			r, openErr := s.open(ctx, id)
			if openErr != nil {
				return openErr
			}
			r.mu.Lock()
			if !jsonEqual(r.core.Load().Configuration().Selection, change.Selection) {
				err = r.core.Load().SelectModel(ctx, change.Selection)
			}
			r.mu.Unlock()
			if err != nil {
				// A preparing request or compaction keeps its current target;
				// the saved intent is retried at the next safe boundary.
				if errors.Is(err, core.ErrBusy) {
					s.wakeSubagents()
				}
				return err
			}
			s.notify(id)
		}
		change.Cursor++
		if err = s.Store.SaveMaintenance(ctx, agentModelPriorityChangeKind, change.AgentID, "running", change); err != nil {
			return err
		}
	}
	return s.Store.DeleteMaintenance(ctx, agentModelPriorityChangeKind, change.AgentID)
}

func normalizeAgentModelPriority(agent *domain.Agent, previous domain.Agent) error {
	prior := AgentModelPriority(previous)
	legacy := core.ModelSelection{ID: agent.DefaultModelID, Reasoning: agent.DefaultReasoning}
	oldLegacy := core.ModelSelection{ID: previous.DefaultModelID, Reasoning: previous.DefaultReasoning}
	priorityChanged := agent.ModelPriority != nil && !jsonEqual(agent.ModelPriority, prior)
	if !priorityChanged && !jsonEqual(legacy, oldLegacy) {
		// A legacy client can still replace the default without dropping the
		// remaining fallback entries. A duplicate later entry is removed.
		agent.ModelPriority = []core.ModelSelection{}
		if legacy.ID != "" {
			agent.ModelPriority = append(agent.ModelPriority, legacy)
			for i, selection := range prior {
				if i > 0 && selection.ID != legacy.ID {
					agent.ModelPriority = append(agent.ModelPriority, selection)
				}
			}
		} else if legacy.Reasoning != nil {
			return domain.Invalid("a reasoning override requires an explicit model")
		}
	} else if agent.ModelPriority == nil {
		agent.ModelPriority = prior
	}
	seen := map[string]bool{}
	for _, selection := range agent.ModelPriority {
		if selection.ID == "" {
			return domain.Invalid("each model priority entry requires a model")
		}
		if seen[selection.ID] {
			return domain.Invalid("duplicate model in model priority")
		}
		seen[selection.ID] = true
	}
	agent.DefaultModelID, agent.DefaultReasoning = "", nil
	if len(agent.ModelPriority) > 0 {
		agent.DefaultModelID = agent.ModelPriority[0].ID
		agent.DefaultReasoning = agent.ModelPriority[0].Reasoning
	}
	return nil
}

func (s *Service) validateAgentModels(ctx context.Context, agent *domain.Agent, previous domain.Agent) error {
	if err := normalizeAgentModelPriority(agent, previous); err != nil {
		return err
	}
	priority := AgentModelPriority(*agent)
	selections := append(priority, core.ModelSelection{ID: agent.ProbeModelID, Reasoning: agent.ProbeReasoning})
	for i, selection := range selections {
		previousSelections := AgentModelPriority(previous)
		if i == len(priority) {
			previousSelections = []core.ModelSelection{{ID: previous.ProbeModelID, Reasoning: previous.ProbeReasoning}}
		}
		unchanged := false
		for _, old := range previousSelections {
			if agent.Revision > 0 && jsonEqual(selection, old) {
				unchanged = true
				break
			}
		}
		// Disabled and removed references retain their positions so the user
		// can repair them without unrelated edits or reordering being blocked.
		if unchanged {
			continue
		}
		if selection.ID == "" {
			if selection.Reasoning != nil {
				return domain.Invalid("a reasoning override requires an explicit model")
			}
			continue
		}
		if _, err := s.Models.ResolveSelection(ctx, selection); err != nil {
			return err
		}
	}
	return nil
}

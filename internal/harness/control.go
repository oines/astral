package harness

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	core "github.com/oines/astral-core"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func (s *Service) SaveAgent(ctx context.Context, agent *domain.Agent) error {
	if agent.Deletion {
		return domain.Invalid("Bot deletion is managed by the delete operation")
	}
	s.agentMu.RLock()
	defer s.agentMu.RUnlock()
	if strings.TrimSpace(agent.Persona) == "" {
		agent.Persona = ""
	}
	if agent.AllowedComputers == nil {
		agent.AllowedComputers = []string{}
	}
	if strings.TrimSpace(agent.Name) == "" {
		return domain.Invalid("Agent name is required")
	}
	if agent.DefaultComputerID != "" && !slices.Contains(agent.AllowedComputers, agent.DefaultComputerID) {
		return domain.Invalid("default Computer must be allowed for this Agent")
	}
	var previous domain.Agent
	if agent.Revision > 0 {
		if err := s.Store.Get(ctx, "agents", agent.ID, &previous); err != nil {
			return err
		}
		if previous.Deletion {
			return domain.Missing("Bot", agent.ID)
		}
	}
	for _, selection := range []struct {
		model             string
		reasoning         *core.ReasoningOptions
		previousModel     string
		previousReasoning *core.ReasoningOptions
	}{
		{agent.DefaultModelID, agent.DefaultReasoning, previous.DefaultModelID, previous.DefaultReasoning},
		{agent.ProbeModelID, agent.ProbeReasoning, previous.ProbeModelID, previous.ProbeReasoning},
	} {
		// Deleted or disabled models leave their saved references intact. An
		// unrelated edit or repair of the other selection must still be possible;
		// only a newly chosen model/reasoning pair needs current validation.
		if agent.Revision > 0 && selection.model == selection.previousModel && jsonEqual(selection.reasoning, selection.previousReasoning) {
			continue
		}
		if selection.model == "" {
			if selection.reasoning != nil {
				return domain.Invalid("a reasoning override requires an explicit model")
			}
			continue
		}
		ref, options, err := s.Models.Selection(ctx, selection.model)
		if err != nil {
			return err
		}
		if selection.reasoning != nil {
			options.Reasoning = *selection.reasoning
			if err = s.Models.ValidateOptions(ctx, ref, options); err != nil {
				return err
			}
		}
	}
	seen := map[string]bool{}
	for _, id := range agent.AllowedComputers {
		if seen[id] {
			return domain.Invalid("duplicate allowed Computer")
		}
		seen[id] = true
		var c domain.Computer
		if err := s.Store.Get(ctx, "computers", id, &c); err != nil {
			return err
		}
		if c.Revoked {
			return domain.Invalid("a revoked Computer cannot be assigned")
		}
	}
	if agent.ID == "" {
		agent.ID = domain.ID("agent")
	}
	err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var current domain.Agent
		if agent.Revision != 0 {
			if err := tx.Get(ctx, "agents", agent.ID, &current); err != nil {
				return err
			}
			if current.Deletion {
				return domain.Missing("Bot", agent.ID)
			}
			if current.Revision != agent.Revision {
				return domain.Conflict("this record changed; reload it before saving")
			}
		}
		// Legacy references may outlive their search service. They must not
		// block unrelated edits, but a new selection must exist at commit time.
		if agent.SearchProviderID != "" && (agent.Revision == 0 || agent.SearchProviderID != current.SearchProviderID) {
			var search domain.SearchProvider
			if err := tx.Get(ctx, "search", agent.SearchProviderID, &search); err != nil {
				return err
			}
		}
		for _, id := range agent.AllowedComputers {
			var computer domain.Computer
			if err := tx.Get(ctx, "computers", id, &computer); err != nil {
				return err
			}
			if computer.Revoked {
				return domain.Invalid("a revoked Computer cannot be assigned")
			}
		}
		return tx.Save(ctx, "agents", &agent.Meta, agent)
	})
	if err == nil && s.Hub != nil {
		s.Hub.ReconcileAgentCommands(ctx, agent.ID)
	}
	return err
}

func (r *runtime) syncBusinessConfig(ctx context.Context) error {
	if _, err := r.readableCore(); err != nil {
		return err
	}
	var record domain.Session
	if err := r.service.Store.Get(ctx, "sessions", r.id, &record); err != nil {
		return err
	}
	if record.Deletion != nil {
		return domain.Missing("Session", r.id)
	}
	if err := r.service.agentAvailable(ctx, record.AgentID); err != nil {
		return err
	}
	if r.subagent != nil {
		if err := r.syncSubagentModel(ctx); err != nil {
			return err
		}
		configuration := r.core.Load().Configuration()
		next := r.subagent.Configuration
		next.Selection = configuration.Selection
		if configuration.SystemPrompt != next.SystemPrompt || !jsonEqual(configuration.Tools, next.Tools) {
			return r.core.Load().Configure(ctx, next)
		}
		return nil
	}
	if err := r.syncBindingConfig(ctx, record); err != nil {
		return err
	}
	configuration := r.core.Load().Configuration()
	definitions := []core.ToolDefinition{}
	for _, tool := range r.hostTools() {
		if tool.Definition().Name != "send_message" || record.BindingID != "" {
			definitions = append(definitions, tool.Definition())
		}
	}
	hostPrompt := systemPromptFor(record)
	if configuration.SystemPrompt != hostPrompt || !jsonEqual(configuration.Tools, definitions) {
		configuration.SystemPrompt = hostPrompt
		configuration.Tools = definitions
		if err := r.core.Load().Configure(ctx, configuration); err != nil {
			return err
		}
	}
	return nil
}
func jsonEqual(a, b any) bool { return string(encode(a)) == string(encode(b)) }

func (s *Service) SelectModel(ctx context.Context, id, profileID string, reasoning ...*core.ReasoningOptions) error {
	selection := core.ModelSelection{ID: profileID}
	if len(reasoning) > 0 {
		selection.Reasoning = reasoning[0]
	}
	return s.selectSubagentModels(ctx, id, selection)
}

func (s *Service) SelectComputer(ctx context.Context, id, computerID, cwd string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, err := r.snapshot()
	if err != nil {
		return err
	}
	if snapshot.Running || snapshot.Maintenance {
		return domain.Conflict("stop the Session before changing its Computer")
	}
	_, err = r.selectComputer(ctx, computerID, cwd)
	if err == nil {
		err = r.refreshEnvironment(ctx)
	}
	return err
}
func (r *runtime) selectComputer(ctx context.Context, id, cwd string) (domain.ExecutionBinding, error) {
	r.environmentMu.Lock()
	defer r.environmentMu.Unlock()
	b, err := r.service.Hub.Bind(ctx, r.agentID, r.id, id, cwd)
	if err != nil {
		return b, err
	}
	var record domain.Session
	if err = r.service.Store.Get(ctx, "sessions", r.id, &record); err != nil {
		return b, err
	}
	record.ComputerID, record.CWD = b.ComputerID, b.CWD
	if err = r.service.Store.Save(ctx, "sessions", &record.Meta, &record); err != nil {
		return b, err
	}
	r.service.notify(r.id)
	return b, nil
}

func (r *runtime) continueLocked(ctx context.Context) error {
	snapshot, err := r.snapshot()
	if err != nil {
		return err
	}
	if snapshot.Running || snapshot.Maintenance {
		return core.ErrBusy
	}
	if err := r.finishInterruptedTools(ctx); err != nil {
		return err
	}
	if err := r.syncBusinessConfig(ctx); err != nil {
		return err
	}
	_, err = r.core.Load().Continue(ctx)
	r.service.notify(r.id)
	return err
}

func (s *Service) Continue(ctx context.Context, id string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	err = r.continueLocked(ctx)
	r.mu.Unlock()
	if err == nil {
		err = s.resumeIMSession(ctx, id)
	}
	return err
}
func (s *Service) Stop(ctx context.Context, id string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.core.Load().Cancel()
	// Serialize retirement of any pending IM nudge with its admission. Cancel
	// first so maintenance holding mu can unwind, then recheck after draining.
	r.mu.Lock()
	r.core.Load().Cancel()
	err = s.Store.DeleteMaintenance(ctx, imReplyWatchKind, id)
	r.mu.Unlock()
	s.notify(id)
	return err
}
func (s *Service) Compact(ctx context.Context, id, instructions string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	err = r.syncBusinessConfig(ctx)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	// Core exposes maintenance state and owns cancellation. The HTTP handler
	// may wait, while the operation is tied to the service rather than its client.
	_, err = r.core.Load().Compact(s.ctx, instructions)
	s.notify(id)
	if err == nil {
		err = s.resumeIMSession(ctx, id)
	}
	return err
}
func (s *Service) ClearPending(ctx context.Context, id, kind string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch kind {
	case "steer":
		err = r.core.Load().ClearSteering(ctx)
	case "follow_up":
		err = r.core.Load().ClearFollowUps(ctx)
	case "all":
		err = r.core.Load().ClearQueuedInputs(ctx)
	default:
		return domain.Invalid("unknown pending-input kind")
	}
	s.notify(id)
	return err
}
func (s *Service) DecideApproval(ctx context.Context, id, approvalID string, decision core.ApprovalDecision) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	err = r.core.Load().DecideApproval(ctx, approvalID, decision)
	s.notify(id)
	return err
}

func (s *Service) Fork(ctx context.Context, id, head, title, newID string) (domain.Session, error) {
	if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
		return domain.Session{}, err
	}
	defer s.subagentMu.Unlock()
	var source domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &source); err != nil {
		return domain.Session{}, err
	}
	release, err := s.LockAgent(ctx, source.AgentID)
	if err != nil {
		return domain.Session{}, err
	}
	defer release()
	parent, err := s.open(ctx, id)
	if err != nil {
		return domain.Session{}, err
	}
	parent.mu.Lock()
	defer parent.mu.Unlock()
	var record domain.Session
	if err = s.Store.Get(ctx, "sessions", id, &record); err != nil {
		return record, err
	}
	if record.Deletion != nil {
		return record, domain.Missing("Session", id)
	}
	if record.SubagentParentID != "" {
		return record, domain.Forbidden("subagent history is read-only; its parent manages delegation")
	}
	if newID == "" {
		return record, domain.Invalid("a stable target Session ID is required for Fork retries")
	}
	var existing domain.Session
	if found := s.Store.Get(ctx, "sessions", newID, &existing); found == nil {
		if existing.ParentSessionID != id || existing.ForkEntryID != head {
			return existing, domain.Conflict("Fork target already belongs to another request")
		}
		return existing, nil
	} else if !domain.IsMissing(found) {
		return record, found
	}
	record.Meta = domain.Meta{ID: newID}
	record.Source, record.ConversationID, record.BindingID, record.ChannelPlatform = "web", "", "", ""
	record.Pinned, record.Archived = false, false
	record.ParentSessionID, record.ForkEntryID = id, head
	record.TitleSource = domain.TitleManual
	if strings.TrimSpace(title) != "" {
		record.Title = title
	} else {
		record.Title += " · fork"
	}
	child, err := parent.core.Load().Fork(ctx, head, core.ForkOptions{ID: newID, Mode: core.ForkDetached})
	if err != nil {
		return record, err
	}
	if err = child.Close(ctx); err != nil {
		return record, err
	}
	if err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := availableAgent(ctx, tx, record.AgentID); err != nil {
			return err
		}
		return tx.Save(ctx, "sessions", &record.Meta, &record)
	}); err != nil {
		return record, err
	}
	s.notify(record.ID)
	return record, nil
}

// SaveSessionPresentation changes presentation fields for the Owner.
func (s *Service) SaveSessionPresentation(ctx context.Context, id string, title *string, pinned, archived, private *bool) error {
	return s.saveSessionPresentation(ctx, "", id, title, pinned, archived, private)
}

// SaveAgentSessionPresentation exposes title and archive actions within one Bot.
// Check ownership before opening a runtime so foreign Sessions stay untouched.
func (s *Service) SaveAgentSessionPresentation(ctx context.Context, agentID, id string, title *string, archived *bool) error {
	if agentID == "" {
		return domain.Invalid("Bot identity is required")
	}
	return s.saveSessionPresentation(ctx, agentID, id, title, nil, archived, nil)
}

func (s *Service) saveSessionPresentation(ctx context.Context, agentID, id string, title *string, pinned, archived, private *bool) error {
	if agentID != "" {
		var record domain.Session
		if err := s.Store.Get(ctx, "sessions", id, &record); err != nil {
			return err
		}
		if record.AgentID != agentID {
			return domain.Forbidden("Session belongs to another Bot")
		}
		if err := persistence.CheckSessionAccess(ctx, record); err != nil {
			return err
		}
		if record.SubagentParentID != "" {
			return domain.Forbidden("subagents are managed through agent_control")
		}
	}
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var record domain.Session
	if err = s.Store.Get(ctx, "sessions", id, &record); err != nil {
		return err
	}
	if record.Deletion != nil {
		return domain.Missing("Session", id)
	}
	if err := persistence.CheckSessionAccess(ctx, record); err != nil {
		return err
	}
	if record.BindingID != "" && archived != nil {
		return domain.Conflict("a bound Session cannot be archived")
	}
	if title != nil {
		if record.BindingID != "" {
			return domain.Conflict("a bound Session uses the chat's title")
		}
		if strings.TrimSpace(*title) == "" {
			return domain.Invalid("title cannot be empty")
		}
		record.Title = *title
		record.TitleSource = domain.TitleManual
	}
	if pinned != nil {
		record.Pinned = *pinned
	}
	if archived != nil {
		record.Archived = *archived
	}
	if private != nil {
		record.Private = *private
	}
	err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.Save(ctx, "sessions", &record.Meta, &record); err != nil {
			return err
		}
		if private == nil {
			return nil
		}
		rows, err := tx.List(ctx, "sessions")
		if err != nil {
			return err
		}
		owned := map[string]bool{id: true}
		remaining := make([]domain.Session, 0, len(rows))
		for _, row := range rows {
			var child domain.Session
			if err = json.Unmarshal(row.Data, &child); err != nil {
				return err
			}
			if child.AgentID == record.AgentID && child.Deletion == nil && child.SubagentParentID != "" {
				remaining = append(remaining, child)
			}
		}
		for len(remaining) > 0 {
			next := remaining[:0]
			changed := false
			for _, child := range remaining {
				if !owned[child.SubagentParentID] {
					next = append(next, child)
					continue
				}
				owned[child.ID] = true
				changed = true
				if child.Private != record.Private {
					child.Private = record.Private
					if err := tx.Save(ctx, "sessions", &child.Meta, &child); err != nil {
						return err
					}
				}
			}
			if !changed {
				break
			}
			remaining = next
		}
		return nil
	})
	s.notify(id)
	return err
}

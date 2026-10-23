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
	if agent.AllowedComputers == nil {
		agent.AllowedComputers = []string{}
	}
	if strings.TrimSpace(agent.Name) == "" {
		return domain.Invalid("Agent name is required")
	}
	if agent.DefaultComputerID != "" && !slices.Contains(agent.AllowedComputers, agent.DefaultComputerID) {
		return domain.Invalid("default Computer must be allowed for this Agent")
	}
	if agent.DefaultModelID != "" {
		if _, _, err := s.Models.Selection(ctx, agent.DefaultModelID); err != nil {
			return err
		}
	}
	if agent.ProbeModelID != "" {
		if _, _, err := s.Models.Selection(ctx, agent.ProbeModelID); err != nil {
			return err
		}
	}
	if agent.SearchProviderID != "" {
		var search domain.SearchProvider
		if err := s.Store.Get(ctx, "search", agent.SearchProviderID, &search); err != nil {
			return err
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
	return s.Store.Save(ctx, "agents", &agent.Meta, agent)
}

func (r *runtime) syncSelection(ctx context.Context) error {
	var record domain.Session
	if err := r.service.Store.Get(ctx, "sessions", r.id, &record); err != nil {
		return err
	}
	ref, options, err := r.service.Models.Selection(ctx, record.ProfileID)
	if err != nil {
		return err
	}
	if record.Reasoning != nil {
		options.Reasoning = *record.Reasoning
	}
	if err = r.syncBindingConfig(ctx, record); err != nil {
		return err
	}
	configuration := r.core.Load().Configuration()
	definitions := []core.ToolDefinition{}
	for _, tool := range r.hostTools() {
		if tool.Definition().Name != "send_message" || record.BindingID != "" {
			definitions = append(definitions, tool.Definition())
		}
	}
	if configuration.SystemPrompt != systemPrompt || !jsonEqual(configuration.Tools, definitions) {
		configuration.SystemPrompt = systemPrompt
		configuration.Tools = definitions
		if err = r.core.Load().Configure(ctx, configuration); err != nil {
			return err
		}
	}
	if ref != r.core.Load().Configuration().ModelID || !jsonEqual(options, r.core.Load().Configuration().Options) {
		if err = r.core.Load().SelectModel(ctx, ref, options); err != nil {
			return err
		}
	}
	if record.ModelRef != ref || !jsonEqual(record.Options, options) {
		record.ModelRef, record.Options = ref, options
		return r.service.Store.Save(ctx, "sessions", &record.Meta, &record)
	}
	return nil
}
func jsonEqual(a, b any) bool { return string(encode(a)) == string(encode(b)) }

func (s *Service) SelectModel(ctx context.Context, id, profileID string, reasoning ...*core.ReasoningOptions) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.core.Load().Snapshot().Maintenance {
		return core.ErrBusy
	}
	ref, options, err := s.Models.Selection(ctx, profileID)
	if err != nil {
		return err
	}
	var record domain.Session
	if err = s.Store.Get(ctx, "sessions", id, &record); err != nil {
		return err
	}
	record.Reasoning = nil
	if len(reasoning) > 0 && reasoning[0] != nil {
		copy := *reasoning[0]
		record.Reasoning, options.Reasoning = &copy, copy
	}
	if err = s.Models.ValidateOptions(ctx, ref, options); err != nil {
		return err
	}
	record.ProfileID, record.ModelRef, record.Options = profileID, ref, options
	// Save the intended selection first. If Core acknowledgement fails, the next
	// Continue reconciles it before dispatch; preparation blocks stale requests.
	if err = s.Store.Save(ctx, "sessions", &record.Meta, &record); err != nil {
		return err
	}
	err = r.core.Load().SelectModel(ctx, ref, options)
	s.notify(id)
	return err
}

func (s *Service) SelectComputer(ctx context.Context, id, computerID, cwd string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.core.Load().Snapshot().Running || r.core.Load().Snapshot().Maintenance {
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
	if snapshot := r.core.Load().Snapshot(); snapshot.Running || snapshot.Maintenance {
		return core.ErrBusy
	}
	if err := r.syncSelection(ctx); err != nil {
		return err
	}
	_, err := r.core.Load().Continue(ctx)
	r.service.notify(r.id)
	return err
}

func (s *Service) Continue(ctx context.Context, id string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.continueLocked(ctx)
}
func (s *Service) Stop(ctx context.Context, id string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.core.Load().Cancel()
	s.notify(id)
	return nil
}
func (s *Service) Compact(ctx context.Context, id, instructions string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	err = r.syncSelection(ctx)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	// Core exposes maintenance state and owns cancellation. The HTTP handler
	// may wait, while the operation is tied to the service rather than its client.
	_, err = r.core.Load().Compact(s.ctx, instructions)
	s.notify(id)
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
func (s *Service) Resolve(ctx context.Context, id, executionID string, execution core.ToolExecution) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.core.Load().Snapshot().Running || r.core.Load().Snapshot().Maintenance {
		return core.ErrBusy
	}
	err = r.core.Load().ResolveTool(ctx, executionID, execution)
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
	if newID == "" {
		return record, domain.Invalid("a stable target Session ID is required for Fork retries")
	}
	var existing domain.Session
	if found := s.Store.Get(ctx, "sessions", newID, &existing); found == nil {
		if existing.ParentSessionID != id || existing.ForkEntryID != head {
			return existing, domain.Conflict("Fork target already belongs to another request")
		}
		if _, err = s.open(ctx, newID); err != nil {
			return existing, err
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
	configuration := child.Configuration()
	record.ModelRef, record.Options = configuration.ModelID, configuration.Options
	record.ProfileID = strings.Split(configuration.ModelID, "~")[0]
	if err = child.Close(ctx); err != nil {
		return record, err
	}
	if err = s.Store.Save(ctx, "sessions", &record.Meta, &record); err != nil {
		return record, err
	}
	err = s.InitializeSession(ctx, record.ID)
	s.notify(record.ID)
	return record, err
}

// SaveSessionPresentation changes only Owner presentation fields.
func (s *Service) SaveSessionPresentation(ctx context.Context, id string, title *string, pinned, archived *bool) error {
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
	err = s.Store.Save(ctx, "sessions", &record.Meta, &record)
	s.notify(id)
	return err
}

func (s *Service) DeleteAgent(ctx context.Context, id string, revision int64) error {
	connections, err := s.Store.List(ctx, "connections")
	if err != nil {
		return err
	}
	for _, item := range connections {
		var c domain.Connection
		if json.Unmarshal(item.Data, &c) == nil && c.AgentID == id {
			return domain.Conflict("this Agent still owns IM accounts")
		}
	}
	items, err := s.Store.List(ctx, "sessions")
	if err != nil {
		return err
	}
	for _, item := range items {
		var value domain.Session
		if json.Unmarshal(item.Data, &value) == nil && value.AgentID == id {
			return domain.Conflict("this Agent still owns retained Sessions; their history requires its configuration")
		}
	}
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.Delete(ctx, "agents", id, revision); err != nil {
			return err
		}
		var setup Onboarding
		if err := tx.Get(ctx, "onboarding", "owner", &setup); err != nil && !domain.IsMissing(err) {
			return err
		}
		if setup.AgentID == id && !setup.Completed {
			setup.Completed = true
			return tx.Save(ctx, "onboarding", &setup.Meta, &setup)
		}
		return nil
	})
}

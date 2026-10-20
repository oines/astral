package harness

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	core "github.com/oines/astral-core"
	sessions "github.com/oines/astral-core/session"
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
	if ref != r.core.Configuration().ModelID || !jsonEqual(options, r.core.Configuration().Options) {
		if err = r.core.SelectModel(ctx, ref, options); err != nil {
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
	if r.maintenance != "" {
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
	err = r.core.SelectModel(ctx, ref, options)
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
	if r.run != nil || r.maintenance != "" {
		return domain.Conflict("stop the Session before changing its Computer")
	}
	_, err = r.selectComputer(ctx, computerID, cwd)
	return err
}
func (r *runtime) selectComputer(ctx context.Context, id, cwd string) (domain.ExecutionBinding, error) {
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
	r.setBinding(b)
	r.service.notify(r.id)
	return b, nil
}

func (r *runtime) continueLocked(ctx context.Context) error {
	if r.run != nil || r.maintenance != "" {
		return core.ErrBusy
	}
	if err := r.syncSelection(ctx); err != nil {
		r.lastError = err.Error()
		return err
	}
	// Readiness can change independently. Bind opportunistically so the prompt
	// contains the actual directories whenever the selected Computer is online.
	_, _ = r.currentBinding(ctx)
	s := r.service
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return core.ErrClosed
	}
	run, err := r.core.Continue(s.ctx)
	if err != nil {
		s.mu.Unlock()
		r.lastError = err.Error()
		return err
	}
	r.trackRunLocked(run)
	s.mu.Unlock()
	s.notify(r.id)
	return nil
}

// The caller holds r.mu and s.mu, covering admission and worker registration.
func (r *runtime) trackRunLocked(run *core.Run) {
	s := r.service
	r.run, r.stopping, r.lastError = run, false, ""
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		result, runErr := run.Wait(context.Background())
		r.mu.Lock()
		if r.run == run {
			stopped := r.stopping
			r.run, r.stopping = nil, false
			if result.Status == "cancelled" {
				r.lastError = ""
			} else if runErr != nil {
				r.lastError = runErr.Error()
			}
			// A queued independent input after no_reply is a new activation.
			// Cancellation and failed execution always require explicit Continue.
			if result.Status == "completed" && runErr == nil && !stopped && s.ctx.Err() == nil {
				if queued, _ := r.core.HasQueuedInputs(s.ctx); queued {
					_ = r.continueLocked(s.ctx)
				}
			}
		}
		r.mu.Unlock()
		s.notify(r.id)
	}()
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
	r.mu.Lock()
	if r.run != nil || r.maintenance != "" {
		r.stopping = true
	}
	r.core.Cancel()
	r.mu.Unlock()
	s.notify(id)
	return nil
}
func (s *Service) Compact(ctx context.Context, id, instructions string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run != nil || r.maintenance != "" {
		return core.ErrBusy
	}
	if err = r.syncSelection(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return core.ErrClosed
	}
	r.maintenance, r.lastError = "compacting", ""
	s.workers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.workers.Done()
		_, err := r.core.Compact(s.ctx, instructions)
		r.mu.Lock()
		r.maintenance, r.stopping = "", false
		if err != nil {
			r.lastError = err.Error()
		}
		r.mu.Unlock()
		s.notify(id)
	}()
	s.notify(id)
	return nil
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
		err = r.core.ClearSteering(ctx)
	case "follow_up":
		err = r.core.ClearFollowUps(ctx)
	case "all":
		err = r.core.ClearQueuedInputs(ctx)
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
	if r.run != nil || r.maintenance != "" {
		return core.ErrBusy
	}
	err = r.core.ResolveTool(ctx, executionID, execution)
	s.notify(id)
	return err
}
func (s *Service) DecideApproval(ctx context.Context, id, approvalID string, decision sessions.ApprovalDecision) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	err = r.core.DecideApproval(ctx, approvalID, decision)
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
	record.Source, record.ConversationID, record.BindingID = "web", "", ""
	record.Pinned, record.Archived = false, false
	record.ParentSessionID, record.ForkEntryID = id, head
	if strings.TrimSpace(title) != "" {
		record.Title = title
	} else {
		record.Title += " · fork"
	}
	child, err := parent.core.Fork(ctx, head, sessions.ForkOptions{ID: newID})
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
	_, err = s.open(ctx, record.ID)
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
		if strings.TrimSpace(*title) == "" {
			return domain.Invalid("title cannot be empty")
		}
		record.Title = *title
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
	return s.Store.Update(ctx, func(tx *persistence.Tx) error { return tx.Delete(ctx, "agents", id, revision) })
}

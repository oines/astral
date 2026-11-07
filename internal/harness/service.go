// Package harness binds persistent Core Sessions to Astral resources.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	core "github.com/oines/astral-core"

	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/recall"
	"github.com/oines/astral/internal/schedule"
	"github.com/oines/astral/internal/search"
	"github.com/oines/astral/internal/timezone"
)

type Service struct {
	Store                *persistence.Store
	CoreStore            core.Store
	Models               *modelbridge.Bridge
	Hub                  *computers.Hub
	Files                *filestream.Service
	Search               *search.Service
	Recall               *recall.Service
	Schedules            *schedule.Service
	Timezone             *timezone.Service
	RuntimeID            string
	IM                   Messenger
	StorageChanged       func()
	ctx                  context.Context
	cancel               context.CancelFunc
	mu                   sync.Mutex
	deleteMu             sync.Mutex
	agentMu              sync.RWMutex
	runtimes             map[string]*runtime
	headTimes            map[string]sessionHeadTime
	selections           map[string]configurationSelection
	childSettings        map[string]subagentMetadata
	closed               bool
	changes              chan string
	sessionChanges       sessionChangeSignal
	workers              sync.WaitGroup
	subagentMu           sync.Mutex
	subagentWake         chan struct{}
	subagentWakeMu       sync.Mutex
	subagentDirty        map[string]struct{}
	subagentRescan       bool
	subagentWorkers      sync.WaitGroup
	modelFallbackMu      sync.Mutex
	modelFallbackWorkers sync.WaitGroup
	modelFallbackProbes  map[string]modelFallbackProbe
}

type runtime struct {
	service          *Service
	id               string
	agentID          string
	prefixRootID     string
	ready            chan struct{}
	openErr          error
	mu               sync.Mutex
	core             atomic.Pointer[core.Session]
	snapshotMu       sync.RWMutex
	corePhase        runtimeCorePhase
	retired          bool
	tools            *standardtools.Set
	environmentMu    sync.Mutex
	processMu        sync.Mutex
	processes        map[string]standardtools.Process
	usageHead        string
	messageTool      core.ToolDefinition
	compactionPrompt string
	contextUsage     *ContextUsage
	titleCancel      context.CancelFunc
	titleDone        chan struct{}
	subagent         *subagentSettings
	imInspector      *IMInspector
}

func New(store *persistence.Store, coreStore core.Store, models *modelbridge.Bridge, hub *computers.Hub) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{Store: store, CoreStore: coreStore, Models: models, Hub: hub,
		Files: &filestream.Service{Store: store, Hub: hub}, Search: &search.Service{Store: store},
		Recall:    recall.New(store, coreStore),
		RuntimeID: domain.ID("server"), ctx: ctx, cancel: cancel, runtimes: map[string]*runtime{}, changes: make(chan string, 256), subagentWake: make(chan struct{}, 1)}
	s.Timezone = timezone.New(store)
	s.Schedules = schedule.New(store, s.deliverSchedule, s.scheduleAccepted, func() { s.notify("") }, s.Timezone)
	s.subagentWorkers.Add(1)
	go s.subagentWorker()
	s.startModelFallbackWorker()
	return s
}
func (s *Service) Changes() <-chan string { return s.changes }
func (s *Service) notify(id string) {
	s.sessionChanges.notify(id)
	// Host operations notify after Core releases its Run/maintenance state.
	// Store commit notifications alone can precede that final idle boundary.
	s.wakeSubagents(id)
	select {
	case s.changes <- id:
	default:
	}
}

func (s *Service) open(ctx context.Context, id string) (*runtime, error) {
	var record domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &record); err != nil {
		return nil, err
	}
	if record.Deletion != nil {
		return nil, domain.Missing("Session", id)
	}
	if err := s.agentAvailable(ctx, record.AgentID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, core.ErrClosed
	}
	if found := s.runtimes[id]; found != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-found.ready:
			return found, found.openErr
		}
	}
	r := &runtime{service: s, id: id, ready: make(chan struct{}), processes: map[string]standardtools.Process{}}
	s.runtimes[id] = r
	s.workers.Add(1)
	s.mu.Unlock()
	// Open is bounded by the caller; a failed open can be retried.
	err := r.initialize(ctx)
	s.mu.Lock()
	r.openErr = err
	close(r.ready)
	if err != nil {
		delete(s.runtimes, id)
	}
	s.mu.Unlock()
	s.workers.Done()
	return r, err
}

func (r *runtime) initialize(ctx context.Context) (err error) {
	s := r.service
	var record domain.Session
	if err = s.Store.Get(ctx, "sessions", r.id, &record); err != nil {
		return err
	}
	if record.Deletion != nil {
		return domain.Missing("Session", r.id)
	}
	r.agentID = record.AgentID
	prefix := record
	seen := map[string]bool{record.ID: true}
	for prefix.SubagentParentID != "" {
		if seen[prefix.SubagentParentID] {
			return domain.Invalid("subagent parent references contain a cycle")
		}
		seen[prefix.SubagentParentID] = true
		var parent domain.Session
		if err = s.Store.Get(ctx, "sessions", prefix.SubagentParentID, &parent); err != nil {
			return err
		}
		prefix = parent
		if prefix.Deletion != nil || prefix.AgentID != record.AgentID {
			return domain.Forbidden("subagent prefix source is unavailable")
		}
	}
	r.prefixRootID = prefix.ID
	if record.SubagentParentID != "" {
		var settings subagentSettings
		if err = s.Store.Get(ctx, "subagent_settings", r.id, &settings); err != nil {
			return err
		}
		r.subagent = &settings
	}
	r.compactionPrompt = compactionPromptFor(record)
	r.messageTool, err = r.messageToolFor(ctx, prefix)
	if err != nil {
		return err
	}
	var agent domain.Agent
	if err = s.Store.Get(ctx, "agents", r.agentID, &agent); err != nil {
		return err
	}
	if agent.Deletion {
		return domain.Missing("Bot", agent.ID)
	}
	r.tools, err = standardtools.New(&trackedBackend{runtime: r, backend: &computers.Backend{Hub: s.Hub}})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if r.core.Load() != nil {
				_ = r.closeCore(cleanup)
			}
			_ = r.tools.Close(cleanup)
		}
	}()
	config := r.coreConfig(prefix)
	session, err := core.OpenSession(ctx, config, r.id)
	created := errors.Is(err, core.ErrNotFound)
	if errors.Is(err, core.ErrNotFound) {
		if r.subagent != nil {
			config.Selection, err = s.availableModelSelection(ctx, agent, r.subagent.Configuration.Selection)
			if err != nil {
				// The child was already accepted while its model was usable.
				// Let Core persist its creation before passive preparation fails,
				// so reconciliation can report an exhausted task to its parent.
				config.Selection = r.subagent.Configuration.Selection
			}
		}
		if r.subagent != nil && r.subagent.InheritContext {
			parent, openErr := s.open(ctx, record.SubagentParentID)
			if openErr != nil {
				return openErr
			}
			child, forkErr := parent.core.Load().Fork(ctx, r.subagent.ForkEntryID, core.ForkOptions{ID: r.id, Mode: core.ForkShared})
			if forkErr != nil {
				return forkErr
			}
			if err = child.Close(ctx); err != nil {
				return err
			}
			session, err = core.OpenSession(ctx, config, r.id)
		} else if record.ParentSessionID != "" {
			parent, openErr := s.open(ctx, record.ParentSessionID)
			if openErr != nil {
				return openErr
			}
			child, forkErr := parent.core.Load().Fork(ctx, record.ForkEntryID, core.ForkOptions{ID: r.id, Mode: core.ForkDetached})
			if forkErr != nil {
				return forkErr
			}
			if closeErr := child.Close(ctx); closeErr != nil {
				return closeErr
			}
			session, err = core.OpenSession(ctx, config, r.id)
		} else {
			if r.subagent == nil {
				config.Selection, err = s.FirstAvailableAgentModel(ctx, agent)
				if err != nil {
					return err
				}
			}
			session, err = core.NewSession(ctx, config, core.SessionOptions{ID: r.id})
		}
	}
	r.core.Store(session)
	if err == nil && created && r.subagent == nil && record.ParentSessionID == "" {
		priority := AgentModelPriority(agent)
		if len(priority) > 0 {
			err = r.initializeModelFallback(ctx, priority[0], config.Selection)
		}
	}
	if err == nil {
		err = r.finishInterruptedTools(ctx)
	}
	// Reopening history must not require the current Provider/Profile to still
	// exist or be enabled. The persisted model reference resolves its immutable
	// snapshot for inspection; execution reconciles the current selection in
	// continueLocked/Compact. Only a newly created Session needs initial setup.
	if err == nil && created {
		if r.subagent != nil {
			err = r.initializeModelFallback(ctx, r.subagent.Configuration.Selection, config.Selection)
		}
	}
	if err == nil && created {
		err = r.syncBusinessConfig(ctx)
	}
	if err == nil && created && r.subagent != nil {
		view, readErr := r.core.Load().ReadContext(ctx)
		if readErr != nil {
			return readErr
		}
		identity, identityErr := r.identityContext(ctx, core.ContextBoundary{SessionID: r.id, Reason: "created", Context: view})
		if identityErr != nil {
			return identityErr
		}
		if len(identity) > 0 {
			_, err = r.core.Load().InjectContext(ctx, core.Input{ID: "subagent_identity_" + r.id, Digest: digest(identity), Contexts: identity})
		}
	}
	if err == nil && r.subagent != nil {
		s.wakeSubagents(r.id)
	}
	return err
}

func (r *runtime) coreConfig(record domain.Session) core.Config {
	s := r.service
	config := core.Config{
		Store: subagentWakeStore{Store: s.CoreStore, service: s}, ResolveModel: r.resolveModel, RestoreModel: r.restoreModel, EstimateTokensReadOnly: modelbridge.EstimateTokens,
		SystemPrompt: systemPromptFor(record), Tools: r.hostTools(),
		Compaction: core.CompactionPolicy{SystemPrompt: r.compactionPrompt}, CollectContextReadOnly: r.collectContext,
		MaxContextBytes: 64 << 20, MaxParallelTools: 8,
		Retry: core.RetryPolicy{MaxAttempts: 5},
	}
	if r.subagent != nil {
		config.Selection = r.subagent.Configuration.Selection
	}
	return config
}

func (r *runtime) resolveModel(ctx context.Context, selection core.ModelSelection) (core.ResolvedModel, error) {
	ctx = modelbridge.WithSessionID(ctx, r.id)
	resolved, err := r.service.Models.ResolveSelection(ctx, selection)
	if err != nil {
		if recordErr := r.recordModelFallbackFailure(ctx, selection, err); recordErr != nil {
			return core.ResolvedModel{}, errors.Join(err, recordErr)
		}
		return core.ResolvedModel{}, err
	}
	resolved.Model = sessionModel{Model: resolved.Model, runtime: r, selection: selection}
	return resolved, nil
}

func (r *runtime) restoreModel(ctx context.Context, snapshot core.ModelSnapshot) (core.Model, error) {
	model, err := r.service.Models.RestoreModel(modelbridge.WithSessionID(ctx, r.id), snapshot)
	if err != nil {
		return nil, err
	}
	profileID, _, _ := strings.Cut(snapshot.Spec.ID, "~")
	return sessionModel{Model: model, runtime: r, selection: core.ModelSelection{ID: profileID}}, nil
}

func (r *runtime) currentBinding(ctx context.Context) (domain.ExecutionBinding, error) {
	var record domain.Session
	if err := r.service.Store.Get(ctx, "sessions", r.id, &record); err != nil {
		return domain.ExecutionBinding{}, err
	}
	if record.ComputerID == "" {
		return domain.ExecutionBinding{}, domain.Conflict("select a Computer before using execution tools")
	}
	b, err := r.service.Hub.Bind(ctx, r.agentID, r.id, record.ComputerID, record.CWD)
	if err == nil {
		err = r.rememberBinding(ctx, b)
	}
	return b, err
}

func (s *Service) Core(ctx context.Context, id string) (*core.Session, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.readableCore()
}
func (s *Service) CreateSession(ctx context.Context, agentID, title string) (domain.Session, error) {
	return s.createSession(ctx, agentID, title, domain.ID("session"), "web", false, false)
}

// createSession also serves scheduled new conversations. Their deterministic
// occurrence ID permits reopening an already created Host record without
// creating a second conversation or copying another Session's history.
func (s *Service) createSession(ctx context.Context, agentID, title, id, source string, reuse, private bool) (domain.Session, error) {
	release, err := s.LockAgent(ctx, agentID)
	if err != nil {
		return domain.Session{}, err
	}
	defer release()
	if reuse {
		var existing domain.Session
		if err := s.Store.Get(ctx, "sessions", id, &existing); err == nil {
			if existing.AgentID != agentID || existing.Source != source || existing.Deletion != nil {
				return domain.Session{}, domain.Conflict("scheduled Session identity is unavailable")
			}
			return existing, nil
		} else if !domain.IsMissing(err) {
			return domain.Session{}, err
		}
	}
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", agentID, &agent); err != nil {
		return domain.Session{}, err
	}
	_, err = s.FirstAvailableAgentModel(ctx, agent)
	if err != nil {
		return domain.Session{}, err
	}
	record := domain.Session{Meta: domain.Meta{ID: id}, AgentID: agentID, Title: strings.TrimSpace(title), TitleSource: domain.TitleManual, Source: source,
		ComputerID: agent.DefaultComputerID, Private: private}
	if defaultSessionTitle(record.Title) {
		record.Title = "New session"
		record.TitleSource = domain.TitleDefault
	}
	if err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := availableAgent(ctx, tx, agentID); err != nil {
			return err
		}
		if reuse {
			var existing domain.Session
			if err := tx.Get(ctx, "sessions", id, &existing); err == nil {
				if existing.AgentID != agentID || existing.Source != source || existing.Deletion != nil {
					return domain.Conflict("scheduled Session identity is unavailable")
				}
				record = existing
				return nil
			} else if !domain.IsMissing(err) {
				return err
			}
		}
		return tx.Save(ctx, "sessions", &record.Meta, &record)
	}); err != nil {
		return record, err
	}
	err = s.InitializeSession(ctx, record.ID)
	if err == nil {
		err = s.Store.Get(ctx, "sessions", record.ID, &record)
		s.notify(record.ID)
	}
	return record, err
}

// FirstAvailableAgentModel skips resources that the owner disabled or removed.
// It validates local configuration only; availability at the provider is learned
// by the task's real requests.
func (s *Service) FirstAvailableAgentModel(ctx context.Context, agent domain.Agent) (core.ModelSelection, error) {
	return s.availableModelSelection(ctx, agent, core.ModelSelection{})
}

func (s *Service) availableModelSelection(ctx context.Context, agent domain.Agent, preferred core.ModelSelection) (core.ModelSelection, error) {
	var firstErr error
	priority := AgentModelPriority(agent)
	if preferred.ID != "" {
		priority = append([]core.ModelSelection{preferred}, priority...)
	}
	seen := map[string]bool{}
	for _, selection := range priority {
		if seen[selection.ID] {
			continue
		}
		seen[selection.ID] = true
		if _, err := s.Models.ResolveSelection(ctx, selection); err == nil {
			return selection, nil
		} else if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return core.ModelSelection{}, firstErr
	}
	return core.ModelSelection{}, domain.Invalid("choose a model for this Bot before creating a Session")
}

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	all := make([]*runtime, 0, len(s.runtimes))
	for _, r := range s.runtimes {
		all = append(all, r)
	}
	s.mu.Unlock()
	// A reconciler can be waiting for admission held by Core maintenance.
	// Cancel that work before joining reconcilers, so maintenance releases
	// admission. Cancel leaves the execution window intact; retirement and
	// Close still run through the lifecycle gates after those workers finish.
	for _, r := range all {
		if session := r.core.Load(); session != nil {
			session.Cancel()
		}
	}
	s.sessionChanges.close()
	var errs []error
	if s.Schedules != nil {
		errs = append(errs, s.Schedules.Close(ctx))
	}
	// A cancelled reconciler can still be finishing durable parent admission.
	// Join it before Close clears any Core window it may inspect. The earlier
	// cancellation releases running work without closing its readable handle.
	subagentsDone := make(chan struct{})
	go func() { s.subagentWorkers.Wait(); s.modelFallbackWorkers.Wait(); close(subagentsDone) }()
	select {
	case <-subagentsDone:
	case <-ctx.Done():
		return errors.Join(append(errs, ctx.Err())...)
	}
	for _, r := range all {
		select {
		case <-r.ready:
		case <-ctx.Done():
			return ctx.Err()
		}
		r.retire()
		// Drain callbacks that received a raw Core handle before retirement.
		// Joining Core must remain outside mu: cancelled tools can need it.
		r.mu.Lock()
		r.mu.Unlock() //nolint:staticcheck // Join pre-retirement callbacks before Core.Close clears their window.
		errs = append(errs, r.closeCore(ctx))
		if r.tools != nil {
			errs = append(errs, r.tools.Close(ctx))
		}
	}
	done := make(chan struct{})
	go func() { s.workers.Wait(); s.subagentWorkers.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		errs = append(errs, ctx.Err())
	}
	errs = append(errs, s.Recall.Close())
	return errors.Join(errs...)
}

func encode(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

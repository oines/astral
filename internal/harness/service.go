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
	Store     *persistence.Store
	CoreStore core.Store
	Models    *modelbridge.Bridge
	Hub       *computers.Hub
	Files     *filestream.Service
	Search    *search.Service
	Recall    *recall.Service
	Schedules *schedule.Service
	Timezone  *timezone.Service
	RuntimeID string
	IM        Messenger
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	deleteMu  sync.Mutex
	agentMu   sync.RWMutex
	runtimes  map[string]*runtime
	closed    bool
	changes   chan string
	workers   sync.WaitGroup
}

type runtime struct {
	service          *Service
	id               string
	agentID          string
	ready            chan struct{}
	openErr          error
	mu               sync.Mutex
	core             atomic.Pointer[core.Session]
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
}

func New(store *persistence.Store, coreStore core.Store, models *modelbridge.Bridge, hub *computers.Hub) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{Store: store, CoreStore: coreStore, Models: models, Hub: hub,
		Files: &filestream.Service{Store: store, Hub: hub}, Search: &search.Service{Store: store},
		Recall:    recall.New(store, coreStore),
		RuntimeID: domain.ID("server"), ctx: ctx, cancel: cancel, runtimes: map[string]*runtime{}, changes: make(chan string, 256)}
	s.Timezone = timezone.New(store)
	s.Schedules = schedule.New(store, s.deliverSchedule, s.scheduleAccepted, func() { s.notify("") }, s.Timezone)
	return s
}
func (s *Service) Changes() <-chan string { return s.changes }
func (s *Service) notify(id string) {
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
	r.compactionPrompt = compactionPromptFor(record)
	r.messageTool, err = r.messageToolFor(ctx, record)
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
				_ = r.core.Load().Close(cleanup)
			}
			_ = r.tools.Close(cleanup)
		}
	}()
	config := r.coreConfig(record)
	session, err := core.OpenSession(ctx, config, r.id)
	created := errors.Is(err, core.ErrNotFound)
	if errors.Is(err, core.ErrNotFound) {
		if record.ParentSessionID != "" {
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
			config.Selection = core.ModelSelection{ID: agent.DefaultModelID, Reasoning: agent.DefaultReasoning}
			session, err = core.NewSession(ctx, config, core.SessionOptions{ID: r.id})
		}
	}
	r.core.Store(session)
	if err == nil {
		err = r.finishInterruptedTools(ctx)
	}
	// Reopening history must not require the current Provider/Profile to still
	// exist or be enabled. The persisted model reference resolves its immutable
	// snapshot for inspection; execution reconciles the current selection in
	// continueLocked/Compact. Only a newly created Session needs initial setup.
	if err == nil && created {
		err = r.syncBusinessConfig(ctx)
	}
	return err
}

func (r *runtime) coreConfig(record domain.Session) core.Config {
	s := r.service
	return core.Config{
		Store: s.CoreStore, ResolveModel: r.resolveModel, RestoreModel: r.restoreModel, EstimateTokens: modelbridge.EstimateTokens,
		SystemPrompt: systemPromptFor(record), Tools: r.hostTools(),
		Compaction: core.CompactionPolicy{SystemPrompt: r.compactionPrompt}, CollectContext: r.collectContext,
		MaxContextBytes: 64 << 20, MaxParallelTools: 8,
	}
}

func (r *runtime) resolveModel(ctx context.Context, selection core.ModelSelection) (core.ResolvedModel, error) {
	ctx = modelbridge.WithSessionID(ctx, r.id)
	resolved, err := r.service.Models.ResolveSelection(ctx, selection)
	if err != nil {
		return core.ResolvedModel{}, err
	}
	resolved.Model = sessionModel{Model: resolved.Model, runtime: r}
	return resolved, nil
}

func (r *runtime) restoreModel(ctx context.Context, snapshot core.ModelSnapshot) (core.Model, error) {
	model, err := r.service.Models.RestoreModel(modelbridge.WithSessionID(ctx, r.id), snapshot)
	if err != nil {
		return nil, err
	}
	return sessionModel{Model: model, runtime: r}, nil
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
	return r.core.Load(), nil
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
	_, _, err = s.Models.Selection(ctx, agent.DefaultModelID)
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

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	all := make([]*runtime, 0, len(s.runtimes))
	for _, r := range s.runtimes {
		all = append(all, r)
	}
	s.mu.Unlock()
	var errs []error
	if s.Schedules != nil {
		errs = append(errs, s.Schedules.Close(ctx))
	}
	for _, r := range all {
		select {
		case <-r.ready:
		case <-ctx.Done():
			return ctx.Err()
		}
		if r.core.Load() != nil {
			errs = append(errs, r.core.Load().Close(ctx))
		}
		if r.tools != nil {
			errs = append(errs, r.tools.Close(ctx))
		}
	}
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		errs = append(errs, ctx.Err())
	}
	errs = append(errs, s.Recall.Close())
	return errors.Join(errs...)
}

func encode(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

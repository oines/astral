// Package harness binds persistent Core Sessions to Astral resources.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	core "github.com/oines/astral-core"
	sessions "github.com/oines/astral-core/session"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/search"
)

type Service struct {
	Store     *persistence.Store
	CoreStore sessions.Store
	Models    *modelbridge.Bridge
	Hub       *computers.Hub
	Files     *filestream.Service
	Search    *search.Service
	RuntimeID string
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	runtimes  map[string]*runtime
	closed    bool
	changes   chan string
	workers   sync.WaitGroup
}

type runtime struct {
	service     *Service
	id          string
	agentID     string
	ready       chan struct{}
	openErr     error
	mu          sync.Mutex
	manager     *sessions.Manager
	core        *sessions.Session
	tools       *standardtools.Set
	run         *core.Run
	stopping    bool
	maintenance string
	lastError   string
	bindingMu   sync.RWMutex
	binding     domain.ExecutionBinding
	processMu   sync.Mutex
	processes   map[string]standardtools.Process
}

func New(store *persistence.Store, coreStore sessions.Store, models *modelbridge.Bridge, hub *computers.Hub) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{Store: store, CoreStore: coreStore, Models: models, Hub: hub,
		Files: &filestream.Service{Store: store, Hub: hub}, Search: &search.Service{Store: store},
		RuntimeID: domain.ID("server"), ctx: ctx, cancel: cancel, runtimes: map[string]*runtime{}, changes: make(chan string, 256)}
}
func (s *Service) Changes() <-chan string { return s.changes }
func (s *Service) notify(id string) {
	select {
	case s.changes <- id:
	default:
	}
}

func (s *Service) open(ctx context.Context, id string) (*runtime, error) {
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
	r.agentID = record.AgentID
	var agent domain.Agent
	if err = s.Store.Get(ctx, "agents", r.agentID, &agent); err != nil {
		return err
	}
	if record.ModelRef == "" {
		return domain.Conflict("select and verify a model before opening this Session")
	}
	r.tools, err = standardtools.New(&trackedBackend{runtime: r, backend: &computers.Backend{Hub: s.Hub}})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if r.manager != nil {
				_ = r.manager.Close(cleanup)
			}
			_ = r.tools.Close(cleanup)
		}
	}()
	// Provision execution context outside request preparation. A disconnected
	// Computer remains visible as unavailable; the model can select another.
	if record.ComputerID != "" {
		if b, bindErr := s.Hub.Bind(ctx, r.agentID, r.id, record.ComputerID, record.CWD); bindErr == nil {
			r.setBinding(b)
		}
	}
	model, err := r.resolveModel(ctx, record.ModelRef)
	if err != nil {
		return err
	}
	r.manager, err = sessions.New(sessions.Config{
		Model: model, Store: s.CoreStore, ResolveModel: r.resolveModel, EstimateTokens: modelbridge.EstimateTokens,
		SystemPrompt: systemPrompt, Options: record.Options, Tools: r.hostTools(),
		Retry:           core.RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Second, MaxBackoff: 5 * time.Second, RequestTimeout: 4 * time.Minute},
		Budget:          sessions.BudgetPolicy{SafetyMargin: 1024, AutoCompactAt: 4096},
		Compaction:      sessions.CompactionPolicy{KeepRecentTokens: 12000, OutputTokens: 4096},
		MaxContextBytes: 64 << 20, MaxParallelTools: 8,
	})
	if err != nil {
		return err
	}
	r.core, err = r.manager.OpenSession(ctx, r.id)
	if errors.Is(err, sessions.ErrNotFound) {
		if record.ParentSessionID != "" {
			parent, openErr := s.open(ctx, record.ParentSessionID)
			if openErr != nil {
				return openErr
			}
			child, forkErr := parent.core.Fork(ctx, record.ForkEntryID, sessions.ForkOptions{ID: r.id})
			if forkErr != nil {
				return forkErr
			}
			if closeErr := child.Close(ctx); closeErr != nil {
				return closeErr
			}
			r.core, err = r.manager.OpenSession(ctx, r.id)
		} else {
			r.core, err = r.manager.NewSession(ctx, sessions.SessionOptions{ID: r.id})
		}
	}
	return err
}

func (r *runtime) resolveModel(ctx context.Context, ref string) (core.Model, error) {
	return r.service.Models.Resolve(ctx, ref, func(ctx context.Context) (string, error) { return r.environment(ctx, ref) })
}
func (r *runtime) setBinding(b domain.ExecutionBinding) {
	r.bindingMu.Lock()
	r.binding = b
	r.bindingMu.Unlock()
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
		r.setBinding(b)
	}
	return b, err
}

func (s *Service) Core(ctx context.Context, id string) (*sessions.Session, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.core, nil
}
func (s *Service) CreateSession(ctx context.Context, agentID, title string) (domain.Session, error) {
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", agentID, &agent); err != nil {
		return domain.Session{}, err
	}
	ref, options, err := s.Models.Selection(ctx, agent.DefaultModelID)
	if err != nil {
		return domain.Session{}, err
	}
	record := domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: agentID, Title: strings.TrimSpace(title), Source: "web",
		ProfileID: agent.DefaultModelID, ModelRef: ref, Options: options, ComputerID: agent.DefaultComputerID}
	if record.Title == "" {
		record.Title = "New session"
	}
	if err = s.Store.Save(ctx, "sessions", &record.Meta, &record); err != nil {
		return record, err
	}
	_, err = s.open(ctx, record.ID)
	if err == nil {
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
	for _, r := range all {
		select {
		case <-r.ready:
		case <-ctx.Done():
			return ctx.Err()
		}
		if r.manager != nil {
			errs = append(errs, r.manager.Close(ctx))
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
	return errors.Join(errs...)
}

func encode(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

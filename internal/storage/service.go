package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type Config struct {
	Directory       string
	Store           *persistence.Store
	Core            core.Store
	Busy            func() bool
	Reclaimers      []func(context.Context) error
	Updating        func(context.Context) bool
	ExternalCleanup func(context.Context) ([]CleanupItem, error)
	CleanExternal   func(context.Context, string, string) (int64, error)
	reclaimInterval time.Duration
}
type sessionEstimate struct {
	Version        uint64
	Rollout        string
	Entries, Extra int64
	State, Error   string
}
type Service struct {
	config            Config
	ctx               context.Context
	cancel            context.CancelFunc
	mu                sync.Mutex
	estimate          Estimate
	cache             map[string]sessionEstimate
	archives          map[string]persistence.ArchiveInfo
	estimateCancel    context.CancelFunc
	reclaim           Reclaim
	reclaimGeneration uint64
	reclaimPersistMu  sync.Mutex
	reclaimDirty      bool
	reclaimStep       int
	reclaimObserved   string
	wake              chan struct{}
	workers           sync.WaitGroup
	cleanupMu         sync.Mutex
	activeCleanup     map[string]bool
	lastSnapshot      *Snapshot
	partialBackups    map[string]bool
}

func New(parent context.Context, config Config) *Service {
	if canonical, err := filepath.EvalSymlinks(config.Directory); err == nil {
		config.Directory = canonical
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{config: config, ctx: ctx, cancel: cancel, estimate: Estimate{State: "idle"}, cache: map[string]sessionEstimate{}, archives: map[string]persistence.ArchiveInfo{}, reclaim: Reclaim{State: "idle"}, wake: make(chan struct{}, 1), activeCleanup: map[string]bool{}, partialBackups: map[string]bool{}}
	if pending, err := config.Store.PendingMaintenance(ctx, cleanupKind); err == nil {
		for _, raw := range pending {
			var record cleanupRecord
			if json.Unmarshal(raw, &record) == nil {
				for _, item := range record.Candidates {
					if item.Started && item.Kind == "upgrade_backup" {
						s.partialBackups[item.Path] = true
					}
				}
			}
		}
	}
	var retained reclaimRecord
	if err := config.Store.Maintenance(ctx, "storage_reclaim", "owner", &retained); err == nil {
		s.reclaim, s.reclaimStep, s.reclaimObserved = retained.Reclaim, retained.Next, retained.ObservedDirtyToken
		if s.reclaim.State == "running" {
			s.reclaim.State = "queued"
			s.reclaimDirty = true
		}
	} else if !domain.IsMissing(err) {
		s.reclaim.State, s.reclaim.Error = "failed", err.Error()
	}
	if err := s.observeReclaimDirty(ctx); err != nil {
		s.reclaim.Error = "Could not inspect pending database reclamation: " + err.Error()
	}
	s.workers.Add(1)
	go s.reclaimWorker()
	return s
}
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.cancel()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
		// The service context is canceled first to release a running VACUUM or
		// blocked reader. Flush its last state with the shutdown owner's still
		// live context, so an already observed failure cannot remain "running"
		// on disk and turn into an automatic retry after a normal restart.
		return s.persistReclaimContext(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func records[T any](ctx context.Context, store *persistence.Store, kind string) ([]T, error) {
	rows, err := store.List(ctx, kind)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		var v T
		if err = json.Unmarshal(r.Data, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) Snapshot(ctx context.Context, agentID string) (Snapshot, error) {
	s.mu.Lock()
	if s.reclaim.State == "running" && s.lastSnapshot != nil {
		out := *s.lastSnapshot
		out.Estimate, out.Reclaim = s.estimate, s.reclaim
		out.Agents = append([]Agent{}, out.Agents...)
		s.mu.Unlock()
		if agentID != "" {
			filtered := out.Agents[:0]
			for _, agent := range out.Agents {
				if agent.AgentID == agentID {
					filtered = append(filtered, agent)
				}
			}
			out.Agents = filtered
		}
		out.Physical = s.physical(ctx)
		return out, nil
	}
	s.mu.Unlock()
	out := Snapshot{Directory: s.config.Directory, Agents: []Agent{}}
	agents, err := records[domain.Agent](ctx, s.config.Store, "agents")
	if err != nil {
		return out, err
	}
	sessions, err := records[domain.Session](ctx, s.config.Store, "sessions")
	if err != nil {
		return out, err
	}
	connections, err := records[domain.Connection](ctx, s.config.Store, "connections")
	if err != nil {
		return out, err
	}
	conversations, err := records[domain.Conversation](ctx, s.config.Store, "conversations")
	if err != nil {
		return out, err
	}
	bindings, err := records[domain.ConversationBinding](ctx, s.config.Store, "bindings")
	if err != nil {
		return out, err
	}
	policies, err := records[struct {
		domain.Meta
		RetentionDays int `json:"retention_days"`
	}](ctx, s.config.Store, "storage_retention")
	if err != nil {
		return out, err
	}
	s.mu.Lock()
	out.Estimate, out.Reclaim = s.estimate, s.reclaim
	cache := make(map[string]sessionEstimate, len(s.cache))
	for k, v := range s.cache {
		cache[k] = v
	}
	archives := make(map[string]persistence.ArchiveInfo, len(s.archives))
	for k, v := range s.archives {
		archives[k] = v
	}
	s.mu.Unlock()
	retentions := map[string]int{}
	for _, policy := range policies {
		retentions[policy.ID] = policy.RetentionDays
	}
	bySession := map[string]domain.Session{}
	for _, v := range sessions {
		if v.Deletion == nil {
			bySession[v.ID] = v
		}
	}
	byConnection := map[string]domain.Connection{}
	for _, v := range connections {
		byConnection[v.ID] = v
	}
	byConversation := map[string]domain.ConversationBinding{}
	for _, v := range bindings {
		if v.Enabled {
			byConversation[v.ConversationID] = v
		}
	}
	for _, agent := range agents {
		if agent.Deletion || agentID != "" && agent.ID != agentID {
			continue
		}
		row := Agent{AgentID: agent.ID, Name: agent.Name, Chats: []Chat{}, Sessions: []Session{}}
		bound := map[string]bool{}
		seenRollout := map[string]bool{}
		add := func(e sessionEstimate) {
			row.LogicalBytes += e.Extra
			if !seenRollout[e.Rollout] {
				row.LogicalBytes += e.Entries
				seenRollout[e.Rollout] = true
			}
		}
		for _, chat := range conversations {
			connection, ok := byConnection[chat.ConnectionID]
			if !ok || connection.AgentID != agent.ID {
				continue
			}
			info, archiveReady := archives[chat.ID]
			v := Chat{ConversationID: chat.ID, ConnectionID: connection.ID, AccountName: connection.Name, Title: chat.Title, Platform: connection.Platform, IMBytes: info.Bytes, EstimateState: "complete"}
			v.RetentionDays, v.ArchiveCount, v.ArchiveOldest, v.ArchiveNewest = retentions[chat.ID], info.Count, info.Oldest, info.Newest
			if binding, ok := byConversation[chat.ID]; ok && binding.AgentID == agent.ID {
				v.BindingID, v.BindingRevision = binding.ID, binding.Revision
				if session, ok := bySession[binding.SessionID]; ok && session.AgentID == agent.ID {
					bound[session.ID] = true
					v.SessionID, v.SessionRevision = session.ID, session.Revision
					if e, ok := cache[session.ID]; ok {
						v.RolloutBytes = e.Entries + e.Extra
						v.EstimateState, v.Error = e.State, e.Error
						add(e)
					} else {
						v.EstimateState = "pending"
					}
				}
			}
			if !archiveReady {
				v.EstimateState = "pending"
			}
			v.LogicalBytes = v.IMBytes + v.RolloutBytes
			row.LogicalBytes += v.IMBytes
			row.Chats = append(row.Chats, v)
		}
		for _, session := range sessions {
			if session.AgentID != agent.ID || session.Deletion != nil || bound[session.ID] {
				continue
			}
			v := Session{SessionID: session.ID, Title: session.Title, Revision: session.Revision, Archived: session.Archived, EstimateState: "pending"}
			if e, ok := cache[session.ID]; ok {
				v.LogicalBytes = e.Entries + e.Extra
				v.EstimateState, v.Error = e.State, e.Error
				add(e)
			}
			row.Sessions = append(row.Sessions, v)
		}
		sort.Slice(row.Chats, func(i, j int) bool { return row.Chats[i].Title < row.Chats[j].Title })
		sort.Slice(row.Sessions, func(i, j int) bool { return row.Sessions[i].Title < row.Sessions[j].Title })
		out.Agents = append(out.Agents, row)
	}
	sort.Slice(out.Agents, func(i, j int) bool { return out.Agents[i].LogicalBytes > out.Agents[j].LogicalBytes })
	out.Physical = s.physical(ctx)
	if out.Estimate.State == "idle" {
		s.Refresh(false)
		s.mu.Lock()
		out.Estimate = s.estimate
		s.mu.Unlock()
	}
	if agentID == "" {
		s.mu.Lock()
		cached := out
		s.lastSnapshot = &cached
		s.mu.Unlock()
	}
	return out, nil
}
func (s *Service) Refresh(cancel bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel {
		if s.estimateCancel != nil {
			s.estimateCancel()
		} else {
			now := time.Now().UTC()
			s.estimate = Estimate{State: "canceled", UpdatedAt: &now}
		}
		return
	}
	if s.estimate.State == "running" || s.ctx.Err() != nil {
		return
	}
	ctx, stop := context.WithTimeout(s.ctx, 2*time.Minute)
	s.estimateCancel = stop
	s.estimate = Estimate{State: "running", UpdatedAt: s.estimate.UpdatedAt}
	s.workers.Add(1)
	go func() { defer s.workers.Done(); defer stop(); s.estimateAll(ctx) }()
}
func (s *Service) estimateAll(ctx context.Context) {
	items, err := records[domain.Session](ctx, s.config.Store, "sessions")
	state := "complete"
	message := ""
	if err != nil {
		state = "failed"
		message = err.Error()
	}
	if err == nil {
		chats, readErr := records[domain.Conversation](ctx, s.config.Store, "conversations")
		if readErr != nil {
			err = readErr
			state, message = "partial", readErr.Error()
		} else {
			for _, chat := range chats {
				if err = ctx.Err(); err != nil {
					break
				}
				info, readErr := s.config.Store.ConversationArchiveInfo(ctx, chat.ID)
				if readErr != nil {
					state, message = "partial", readErr.Error()
					continue
				}
				s.mu.Lock()
				s.archives[chat.ID] = info
				s.mu.Unlock()
			}
		}
	}
	for _, session := range items {
		if err = ctx.Err(); err != nil {
			break
		}
		if session.Deletion != nil {
			continue
		}
		bound, stop := context.WithTimeout(ctx, 30*time.Second)
		e := s.measureSession(bound, session.ID)
		stop()
		s.mu.Lock()
		s.cache[session.ID] = e
		s.mu.Unlock()
		if e.State != "complete" {
			state = "partial"
			message = "Some histories could not be fully estimated; inspect the affected Session."
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			state = "canceled"
		} else {
			state = "partial"
		}
		message = err.Error()
	}
	now := time.Now().UTC()
	s.mu.Lock()
	s.estimate = Estimate{State: state, UpdatedAt: &now, Error: message}
	s.estimateCancel = nil
	s.mu.Unlock()
}
func jsonBytes(v any) (int64, error) { raw, err := json.Marshal(v); return int64(len(raw)), err }
func (s *Service) measureSession(ctx context.Context, id string) sessionEstimate {
	e := sessionEstimate{State: "complete"}
	fail := func(err error) sessionEstimate { e.State = "partial"; e.Error = err.Error(); return e }
	state, err := s.config.Core.LoadState(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		return e
	}
	if err != nil {
		return fail(err)
	}
	e.Version, e.Rollout = state.Version, state.RolloutID
	s.mu.Lock()
	old, ok := s.cache[id]
	s.mu.Unlock()
	if ok && old.Version == state.Version && old.Rollout == state.RolloutID && old.State == "complete" {
		return old
	}
	if e.Extra, err = jsonBytes(state); err != nil {
		return fail(err)
	}
	// Public paging prevents an unbounded history read. An indivisible oversized
	// record gets a bounded larger page; beyond 64 MiB we keep an explicit partial
	// estimate rather than bypassing Core's budget or spinning on the cursor.
	cursor := ""
	seen := map[string]bool{}
	for {
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		var page core.Page
		for budget := core.DefaultPageBytes; ; budget *= 2 {
			page, err = s.config.Core.Scan(ctx, state.RolloutID, core.Query{Cursor: cursor, Limit: core.DefaultPageSize, MaxBytes: budget})
			if !errors.Is(err, core.ErrBudget) || budget >= 64<<20 {
				break
			}
		}
		if err != nil {
			return fail(err)
		}
		for _, r := range page.Records {
			n, x := jsonBytes(r)
			if x != nil {
				return fail(x)
			}
			e.Entries += n
		}
		if page.Next == "" {
			break
		}
		if page.Next == cursor || seen[page.Next] || len(page.Records) == 0 {
			return fail(fmt.Errorf("history pagination made no progress"))
		}
		seen[page.Next] = true
		cursor = page.Next
	}
	cursor = ""
	seen = map[string]bool{}
	for {
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		var page core.AttemptPage
		for budget := core.DefaultPageBytes; ; budget *= 2 {
			page, err = s.config.Core.ScanAttempts(ctx, id, core.AttemptQuery{Cursor: cursor, Limit: core.DefaultPageSize, MaxBytes: budget})
			if !errors.Is(err, core.ErrBudget) || budget >= 64<<20 {
				break
			}
		}
		if err != nil {
			return fail(err)
		}
		for _, a := range page.Attempts {
			n, x := jsonBytes(a)
			if x != nil {
				return fail(x)
			}
			e.Extra += n
		}
		if page.Next == "" {
			break
		}
		if page.Next == cursor || seen[page.Next] || len(page.Attempts) == 0 {
			return fail(fmt.Errorf("attempt pagination made no progress"))
		}
		seen[page.Next] = true
		cursor = page.Next
	}
	latest, err := s.config.Core.LoadState(ctx, id)
	if err != nil {
		return fail(err)
	}
	if latest.Version != state.Version {
		return fail(fmt.Errorf("Session changed while estimating; refresh after it becomes idle"))
	}
	return e
}

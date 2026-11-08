package wake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const flowKind = "wake_buffer"
const stateKind = "wake_status"
const receiptKind = "wake_receipt"
const ReceiptRetention = 7 * 24 * time.Hour

type computerKey struct{}

// WithComputerID is set only from a validated command or tunnel binding.
func WithComputerID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, computerKey{}, id)
}

type LimitError struct{ RetryAfter int }

func (e *LimitError) Error() string {
	return "event endpoint is temporarily blocked because its pending batch reached capacity"
}

type endpointState struct {
	AgentID        string    `json:"agent_id"`
	SessionID      string    `json:"session_id"`
	EndpointID     string    `json:"endpoint_id"`
	BlockedUntil   time.Time `json:"blocked_until"`
	LastReceivedAt time.Time `json:"last_received_at"`
	LastStatus     string    `json:"last_status"`
	LastError      string    `json:"last_error,omitempty"`
}
type flow struct {
	AgentID    string       `json:"agent_id"`
	SessionID  string       `json:"session_id"`
	Generation uint64       `json:"generation"`
	Buffer     []BatchEvent `json:"buffer,omitempty"`
	Bytes      int          `json:"bytes"`
	Due        time.Time    `json:"due"`
	Flood      bool         `json:"flood,omitempty"`
	Inflight   *Batch       `json:"inflight,omitempty"`
}
type eventReceipt struct {
	AgentID    string  `json:"agent_id"`
	SessionID  string  `json:"session_id"`
	EndpointID string  `json:"endpoint_id"`
	Digest     string  `json:"digest"`
	ExpiresAt  int64   `json:"expires_at"`
	Receipt    Receipt `json:"receipt"`
}

type Service struct {
	store       *persistence.Store
	deliver     Deliver
	changed     func()
	mu          sync.Mutex
	cache       map[string]domain.WakeEndpoint
	states      map[string]endpointState
	flows       map[string]*flow
	dirty       map[string]struct{}
	retry       map[string]time.Time
	generation  uint64
	computerURL func(string) string
	subscribers map[chan struct{}]struct{}
	wake        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	started     bool
	closed      bool
	now         func() time.Time
	pruneAt     time.Time
	notifyAt    time.Time
}

func New(store *persistence.Store, deliver Deliver, changed func()) *Service {
	return &Service{store: store, deliver: deliver, changed: changed, cache: map[string]domain.WakeEndpoint{}, states: map[string]endpointState{}, flows: map[string]*flow{}, dirty: map[string]struct{}{}, retry: map[string]time.Time{}, generation: uint64(time.Now().UnixNano()), subscribers: map[chan struct{}]struct{}{}, wake: make(chan struct{}, 1), now: time.Now}
}
func (s *Service) SetComputerURLResolver(resolve func(string) string) {
	s.mu.Lock()
	s.computerURL = resolve
	s.mu.Unlock()
}
func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) WakeSession(id string) {
	s.mu.Lock()
	tracked := s.flows[id] != nil
	if tracked {
		s.dirty[id] = struct{}{}
	}
	s.mu.Unlock()
	if tracked {
		s.signal()
	}
}
func (s *Service) broadcast() {
	s.mu.Lock()
	for c := range s.subscribers {
		select {
		case c <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
}

func (s *Service) configurationChanged() {
	s.broadcast()
	if s.changed != nil {
		s.changed()
	}
}
func (s *Service) Subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	return ch, func() { once.Do(func() { s.mu.Lock(); delete(s.subscribers, ch); s.mu.Unlock() }) }
}
func requireController(ctx context.Context, tx interface {
	Get(context.Context, string, string, any) error
}) error {
	scope, ok := persistence.CurrentSessionScope(ctx)
	if !ok {
		return nil
	}
	var caller domain.Session
	if err := tx.Get(ctx, "sessions", scope.SessionID, &caller); err != nil {
		return err
	}
	if caller.AgentID != scope.AgentID || caller.Deletion != nil {
		return domain.Missing("Session", scope.SessionID)
	}
	if caller.SubagentParentID != "" {
		return domain.Forbidden("subagents cannot use event endpoint management")
	}
	return nil
}
func target(ctx context.Context, tx interface {
	Get(context.Context, string, string, any) error
}, agentID, id string) error {
	var record domain.Session
	if err := tx.Get(ctx, "sessions", id, &record); err != nil {
		return err
	}
	if record.AgentID != agentID || record.Deletion != nil {
		return domain.Missing("Session", id)
	}
	if record.SubagentParentID != "" {
		return domain.Forbidden("event endpoints cannot target subagent Sessions")
	}
	return persistence.CheckSessionAccess(ctx, record)
}
func authorize(ctx context.Context, tx interface {
	Get(context.Context, string, string, any) error
}, e domain.WakeEndpoint) error {
	return target(ctx, tx, e.AgentID, e.SessionID)
}
func validate(e domain.WakeEndpoint) error {
	if strings.TrimSpace(e.Title) == "" || utf8.RuneCountInString(e.Title) > 200 {
		return domain.Invalid("an endpoint title of at most 200 characters is required")
	}
	if len(e.Prompt) > 1024 || len(e.Notes) > 1024 {
		return domain.Invalid("prompt and notes must each fit within 1 KiB")
	}
	return nil
}
func (s *Service) presentLocked(e domain.WakeEndpoint) domain.WakeEndpoint {
	if state, ok := s.states[e.ID]; ok {
		if state.BlockedUntil.After(s.now()) {
			v := state.BlockedUntil
			e.BlockedUntil = &v
		}
		if !state.LastReceivedAt.IsZero() {
			v := state.LastReceivedAt
			e.LastReceivedAt = &v
		}
		e.LastStatus, e.LastError = state.LastStatus, state.LastError
	}
	if s.computerURL != nil {
		if base := s.computerURL(e.ComputerID); base != "" {
			e.TriggerURL = strings.TrimRight(base, "/") + "/hooks/" + e.ID
		}
	}
	return e
}
func (s *Service) loadStateLocked(ctx context.Context, id string) error {
	if _, ok := s.states[id]; ok {
		return nil
	}
	var state endpointState
	err := s.store.Maintenance(ctx, stateKind, id, &state)
	if domain.IsMissing(err) {
		return nil
	}
	if err == nil {
		s.states[id] = state
	}
	return err
}
func (s *Service) Create(ctx context.Context, agentID string, input CreateInput) (domain.WakeEndpoint, error) {
	scope, ok := persistence.CurrentSessionScope(ctx)
	computerID, _ := ctx.Value(computerKey{}).(string)
	if !ok || scope.AgentID != agentID || computerID == "" {
		return domain.WakeEndpoint{}, domain.Forbidden("only a root Agent command can create event endpoints")
	}
	if input.SessionID == "" {
		input.SessionID = scope.SessionID
	}
	e := domain.WakeEndpoint{Meta: domain.Meta{ID: domain.ID("wake")}, AgentID: agentID, ComputerID: computerID, SessionID: input.SessionID, SourceSessionID: scope.SessionID, Title: strings.TrimSpace(input.Title), Prompt: input.Prompt, Notes: input.Notes, Enabled: true}
	if input.Enabled != nil {
		e.Enabled = *input.Enabled
	}
	if err := validate(e); err != nil {
		return e, err
	}
	s.mu.Lock()
	if s.computerURL != nil && s.computerURL(computerID) == "" {
		s.mu.Unlock()
		return e, domain.Conflict("the Computer must be online with a Daemon supporting the persistent local API")
	}
	err := s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := requireController(ctx, tx); err != nil {
			return err
		}
		if err := tx.RequireActiveAgent(ctx, agentID); err != nil {
			return err
		}
		if err := target(ctx, tx, agentID, e.SessionID); err != nil {
			return err
		}
		var a domain.Agent
		if err := tx.Get(ctx, "agents", agentID, &a); err != nil {
			return err
		}
		if !slices.Contains(a.AllowedComputers, computerID) {
			return domain.Forbidden("the endpoint Computer is not allowed for this Bot")
		}
		var c domain.Computer
		if err := tx.Get(ctx, "computers", computerID, &c); err != nil {
			return err
		}
		if c.Revoked {
			return domain.Forbidden("Computer is revoked")
		}
		return tx.Save(ctx, Kind, &e.Meta, &e)
	})
	if err == nil {
		s.cache[e.ID] = e
		e = s.presentLocked(e)
	}
	s.mu.Unlock()
	if err == nil {
		s.configurationChanged()
	}
	return e, err
}
func (s *Service) List(ctx context.Context, agentID string) ([]domain.WakeEndpoint, error) {
	if err := requireController(ctx, s.store); err != nil {
		return nil, err
	}
	rows, err := s.store.List(ctx, Kind)
	if err != nil {
		return nil, err
	}
	result := []domain.WakeEndpoint{}
	for _, row := range rows {
		var e domain.WakeEndpoint
		if err = json.Unmarshal(row.Data, &e); err != nil {
			return nil, err
		}
		if agentID != "" && e.AgentID != agentID {
			continue
		}
		if err = authorize(ctx, s.store, e); domain.IsMissing(err) || isForbidden(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		s.mu.Lock()
		err = s.loadStateLocked(ctx, e.ID)
		e = s.presentLocked(e)
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}
func isForbidden(err error) bool { var e *domain.Error; return errors.As(err, &e) && e.Status == 403 }
func (s *Service) Get(ctx context.Context, agentID, id string) (domain.WakeEndpoint, error) {
	if err := requireController(ctx, s.store); err != nil {
		return domain.WakeEndpoint{}, err
	}
	var e domain.WakeEndpoint
	if err := s.store.Get(ctx, Kind, id, &e); err != nil {
		return e, err
	}
	if agentID != "" && e.AgentID != agentID {
		return e, domain.Missing("Wake endpoint", id)
	}
	if err := authorize(ctx, s.store, e); err != nil {
		return e, err
	}
	s.mu.Lock()
	err := s.loadStateLocked(ctx, id)
	e = s.presentLocked(e)
	s.mu.Unlock()
	return e, err
}
func (s *Service) Update(ctx context.Context, agentID, id string, p Patch) (domain.WakeEndpoint, error) {
	s.mu.Lock()
	var e domain.WakeEndpoint
	err := s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := requireController(ctx, tx); err != nil {
			return err
		}
		if err := tx.Get(ctx, Kind, id, &e); err != nil {
			return err
		}
		if agentID != "" && e.AgentID != agentID {
			return domain.Missing("Wake endpoint", id)
		}
		if err := authorize(ctx, tx, e); err != nil {
			return err
		}
		if e.Revision != p.Revision {
			return domain.Conflict("endpoint changed; reload before editing")
		}
		if p.Title != nil {
			e.Title = strings.TrimSpace(*p.Title)
		}
		if p.Prompt != nil {
			e.Prompt = *p.Prompt
		}
		if p.Notes != nil {
			e.Notes = *p.Notes
		}
		if p.Enabled != nil {
			e.Enabled = *p.Enabled
		}
		if err := validate(e); err != nil {
			return err
		}
		return tx.Save(ctx, Kind, &e.Meta, &e)
	})
	if err == nil {
		s.cache[id] = e
		e = s.presentLocked(e)
	}
	s.mu.Unlock()
	if err == nil {
		s.configurationChanged()
	}
	return e, err
}
func (s *Service) Delete(ctx context.Context, agentID, id string, revision int64) error {
	s.mu.Lock()
	err := s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := requireController(ctx, tx); err != nil {
			return err
		}
		var e domain.WakeEndpoint
		if err := tx.Get(ctx, Kind, id, &e); err != nil {
			return err
		}
		if agentID != "" && e.AgentID != agentID {
			return domain.Missing("Wake endpoint", id)
		}
		if err := authorize(ctx, tx, e); err != nil {
			return err
		}
		if err := tx.Purge(ctx, Kind, id, revision); err != nil {
			return err
		}
		return tx.DeleteMaintenance(ctx, stateKind, id)
	})
	if err == nil {
		delete(s.cache, id)
		delete(s.states, id)
	}
	s.mu.Unlock()
	if err == nil {
		s.configurationChanged()
	}
	return err
}
func (s *Service) checkLocked(ctx context.Context, computerID, id string) (domain.WakeEndpoint, error) {
	e, ok := s.cache[id]
	if !ok {
		var loaded domain.WakeEndpoint
		if err := s.store.Get(ctx, Kind, id, &loaded); err != nil {
			return loaded, err
		}
		e = loaded
		s.cache[id] = e
	}
	if e.ComputerID != computerID {
		return e, domain.Forbidden("this endpoint belongs to another Computer")
	}
	if !e.Enabled {
		return e, domain.Conflict("event endpoint is paused")
	}
	if err := s.loadStateLocked(ctx, id); err != nil {
		return e, err
	}
	if blocked := s.states[id].BlockedUntil; blocked.After(s.now()) {
		return e, &LimitError{RetryAfter: max(1, int(blocked.Sub(s.now()).Seconds()+.999))}
	}
	return e, nil
}
func (s *Service) Check(ctx context.Context, computerID, id string) (domain.WakeEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkLocked(ctx, computerID, id)
}
func eventKey(endpoint, id string) string {
	sum := sha256.Sum256([]byte(endpoint + "\x00" + id))
	return hex.EncodeToString(sum[:])
}
func hash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func clip(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	marker := "\n[content truncated]\n"
	if limit <= len(marker) {
		end := max(0, limit)
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		return text[:end], true
	}
	budget := max(0, limit-len(marker))
	head, tail := budget*3/4, budget-budget*3/4
	for head > 0 && !utf8.RuneStart(text[head]) {
		head--
	}
	start := len(text) - tail
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[:head] + marker + text[start:], true
}
func (s *Service) Accept(ctx context.Context, computerID, id string, event Event) (Receipt, error) {
	if err := s.Start(context.Background()); err != nil {
		return Receipt{}, err
	}
	if _, err := s.Check(ctx, computerID, id); err != nil {
		return Receipt{}, err
	}
	if strings.TrimSpace(event.EventID) == "" || len(event.EventID) > 200 || !utf8.ValidString(event.EventID) {
		return Receipt{}, domain.Invalid("event_id of at most 200 bytes is required")
	}
	if !utf8.ValidString(event.Text) || strings.TrimSpace(event.Text) == "" && (len(event.Data) == 0 || string(event.Data) == "null") {
		return Receipt{}, domain.Invalid("event text or data is required")
	}
	var data any
	if len(event.Data) > 0 {
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return Receipt{}, domain.Invalid("invalid event data")
		}
	}
	event.Data, _ = json.Marshal(data)
	if data == nil {
		event.Data = nil
	}
	raw, _ := json.Marshal(event)
	if len(raw) > MaxRequestBytes {
		return Receipt{}, &domain.Error{Status: 413, Code: "too_large", Message: "event request exceeds 64 KiB"}
	}
	body := event.Text
	if data != nil {
		if body != "" {
			body += "\n"
		}
		body += "data: " + string(event.Data)
	}
	body, truncated := clip(body, MaxEventBytes)
	s.mu.Lock()
	e, err := s.checkLocked(ctx, computerID, id)
	if err != nil {
		s.mu.Unlock()
		return Receipt{}, err
	}
	key, digest := eventKey(id, event.EventID), hash(event)
	var prior eventReceipt
	err = s.store.Maintenance(ctx, receiptKind, key, &prior)
	if err == nil && prior.ExpiresAt > s.now().Unix() {
		s.mu.Unlock()
		if prior.Digest != digest {
			return Receipt{}, domain.Conflict("event_id was used for different content")
		}
		return prior.Receipt, nil
	}
	if err != nil && !domain.IsMissing(err) {
		s.mu.Unlock()
		return Receipt{}, err
	}
	now := s.now().UTC()
	f := s.flows[e.SessionID]
	if f == nil {
		s.generation++
		f = &flow{AgentID: e.AgentID, SessionID: e.SessionID, Generation: s.generation}
	}
	next := *f
	next.Buffer = append([]BatchEvent(nil), f.Buffer...)
	// The complete accepted buffer, including each event, is bounded before disk writes.
	configurationBytes := len(e.Title) + len(e.Prompt) + len(e.Notes)
	for _, buffered := range next.Buffer {
		if buffered.EndpointID == e.ID {
			configurationBytes = 0
			break
		}
	}
	remaining := MaxBatchBytes - next.Bytes - configurationBytes
	if remaining <= 0 || len(next.Buffer) >= MaxBatchEvents {
		s.mu.Unlock()
		return Receipt{}, &LimitError{RetryAfter: 1}
	}
	batchFull := len(body) > remaining
	if batchFull {
		body, _ = clip(body, remaining)
		truncated = true
	}
	var c domain.Computer
	receipt := Receipt{EventID: event.EventID, ReceiptID: key, SessionID: e.SessionID, Status: "accepted", Truncated: truncated}
	state := s.states[id]
	state.AgentID, state.SessionID, state.EndpointID = e.AgentID, e.SessionID, id
	state.LastReceivedAt = now
	state.LastStatus = "accepted"
	state.LastError = ""
	err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		var current domain.WakeEndpoint
		if err := tx.Get(ctx, Kind, id, &current); err != nil {
			return err
		}
		if current.Revision != e.Revision || !current.Enabled {
			return domain.Conflict("event endpoint changed; retry")
		}
		if err := tx.RequireActiveAgent(ctx, e.AgentID); err != nil {
			return err
		}
		if err := target(ctx, tx, e.AgentID, e.SessionID); err != nil {
			return err
		}
		var a domain.Agent
		if err := tx.Get(ctx, "agents", e.AgentID, &a); err != nil {
			return err
		}
		if !slices.Contains(a.AllowedComputers, computerID) {
			return domain.Forbidden("Computer is no longer allowed for this Bot")
		}
		if err := tx.Get(ctx, "computers", computerID, &c); err != nil {
			return err
		}
		if c.Revoked {
			return domain.Forbidden("Computer is revoked")
		}
		next.Buffer = append(next.Buffer, BatchEvent{EndpointID: id, ComputerID: computerID, ComputerName: c.Name, EventID: event.EventID, Title: e.Title, Prompt: e.Prompt, Notes: e.Notes, Text: body, ReceivedAt: now, Truncated: truncated})
		next.Bytes += len(body) + configurationBytes
		if next.Due.IsZero() {
			next.Due = now.Add(MergeWindow)
		}
		if batchFull || len(next.Buffer) >= MaxBatchEvents || next.Bytes >= MaxBatchBytes {
			next.Flood = true
			next.Due = now
			state.BlockedUntil = now.Add(BlockDuration)
			state.LastStatus = "blocked"
		}
		r := eventReceipt{AgentID: e.AgentID, SessionID: e.SessionID, EndpointID: id, Digest: digest, ExpiresAt: now.Add(ReceiptRetention).Unix(), Receipt: receipt}
		if err := tx.SaveMaintenance(ctx, receiptKind, key, "complete", r); err != nil {
			return err
		}
		if err := tx.SaveMaintenance(ctx, stateKind, id, "active", state); err != nil {
			return err
		}
		return tx.SaveMaintenance(ctx, flowKind, e.SessionID, "pending", next)
	})
	if err == nil {
		s.flows[e.SessionID] = &next
		s.states[id] = state
	}
	notify := err == nil && (!s.notifyAt.After(now) || next.Flood)
	if notify {
		s.notifyAt = now.Add(time.Second)
	}
	s.mu.Unlock()
	if err == nil {
		s.WakeSession(e.SessionID)
		if notify {
			s.broadcast()
		}
	}
	return receipt, err
}

// BatchActive is called under Harness admission. This service never holds mu
// across a delivery callback, keeping Stop and delivery lock ordering consistent.
func (s *Service) BatchActive(b Batch) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flows[b.SessionID]
	return f != nil && f.Generation == b.Generation
}
func (s *Service) StopSession(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flows[id]
	if f == nil {
		return nil
	}
	s.generation++
	next := flow{AgentID: f.AgentID, SessionID: id, Generation: s.generation}
	if err := s.store.SaveMaintenance(ctx, flowKind, id, "complete", next); err != nil {
		return err
	}
	s.flows[id] = &next
	delete(s.retry, id)
	delete(s.dirty, id)
	return nil
}

// ForgetSession is the in-memory half of permanent Session deletion. Database
// cleanup belongs to the deletion transaction; callbacks cannot recreate it.
func (s *Service) ForgetSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.flows, id)
	delete(s.retry, id)
	delete(s.dirty, id)
	for key, e := range s.cache {
		if e.SessionID == id {
			delete(s.cache, key)
		}
	}
	for key, state := range s.states {
		if state.SessionID == id {
			delete(s.states, key)
		}
	}
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return domain.Offline("event service is closed")
	}
	if s.started {
		return nil
	}
	rows, err := s.store.PendingMaintenance(ctx, flowKind)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var f flow
		if err = json.Unmarshal(row, &f); err != nil {
			return err
		}
		s.flows[f.SessionID] = &f
		s.dirty[f.SessionID] = struct{}{}
		s.generation = max(s.generation, f.Generation)
	}
	s.ctx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))
	s.done = make(chan struct{})
	s.started = true
	go s.worker()
	s.signal()
	return nil
}
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.cancel()
	done := s.done
	s.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Service) worker() {
	defer close(s.done)
	watcher := s.store.Watch(persistence.ChangeKey{Kind: Kind}, persistence.ChangeKey{Kind: "sessions"}, persistence.ChangeKey{Kind: "agents"}, persistence.ChangeKey{Kind: "computers"})
	defer watcher.Close()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		changes := watcher.Changes()
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-timer.C:
		case <-changes:
			s.mu.Lock()
			clear(s.cache)
			for id := range s.flows {
				s.dirty[id] = struct{}{}
			}
			s.mu.Unlock()
			s.broadcast()
		}
		delay := s.cycle(s.ctx)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}
}
func (s *Service) cycle(ctx context.Context) time.Duration {
	s.mu.Lock()
	now := s.now()
	ids := make([]string, 0, len(s.dirty))
	selected := map[string]bool{}
	for id := range s.dirty {
		ids = append(ids, id)
		selected[id] = true
	}
	clear(s.dirty)
	for id, f := range s.flows {
		if !selected[id] && ((!s.retry[id].IsZero() && !s.retry[id].After(now)) || (f.Inflight == nil && len(f.Buffer) > 0 && !f.Due.After(now))) {
			ids = append(ids, id)
		}
	}
	prune := !s.pruneAt.After(now)
	if prune {
		s.pruneAt = now.Add(time.Hour)
	}
	s.mu.Unlock()
	if prune {
		_ = s.store.PruneWakeReceipts(ctx, now.Unix())
	}
	for _, id := range ids {
		s.dispatch(ctx, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delay := time.Hour
	for id, f := range s.flows {
		if retry := s.retry[id]; !retry.IsZero() {
			delay = min(delay, max(time.Millisecond, retry.Sub(now)))
		}
		if len(f.Buffer) > 0 && f.Inflight == nil {
			delay = min(delay, max(time.Millisecond, f.Due.Sub(now)))
		}
	}
	return delay
}
func (s *Service) dispatch(ctx context.Context, id string) {
	s.mu.Lock()
	f := s.flows[id]
	if f == nil {
		s.mu.Unlock()
		return
	}
	if f.Inflight == nil && len(f.Buffer) > 0 && !f.Due.After(s.now()) {
		next := *f
		b := Batch{ID: domain.ID("wake_batch"), AgentID: f.AgentID, SessionID: id, Events: f.Buffer, Flood: f.Flood, Time: s.now().UTC(), Generation: f.Generation}
		b.Digest = hash(b)
		next.Inflight = &b
		next.Buffer = nil
		next.Bytes = 0
		next.Due = time.Time{}
		next.Flood = false
		if err := s.store.SaveMaintenance(ctx, flowKind, id, "pending", next); err != nil {
			s.retry[id] = s.now().Add(time.Second)
			s.mu.Unlock()
			return
		}
		s.flows[id] = &next
		f = &next
	}
	if f.Inflight == nil || s.deliver == nil {
		delete(s.retry, id)
		s.mu.Unlock()
		return
	}
	batch := *f.Inflight
	s.mu.Unlock()
	result, err := s.deliver(ctx, batch)
	s.mu.Lock()
	f = s.flows[id]
	if f == nil || f.Generation != batch.Generation || f.Inflight == nil || f.Inflight.ID != batch.ID {
		s.mu.Unlock()
		return
	}
	delete(s.retry, id)
	if !result.Accepted && !result.Handled && err == nil {
		s.retry[id] = s.now().Add(100 * time.Millisecond)
	}
	next := *f
	inflight := *f.Inflight
	next.Inflight = &inflight
	if result.Accepted {
		inflight.Events = nil
		inflight.Flood = false
	}
	if result.Handled {
		next.Inflight = nil
	}
	if err != nil {
		var problem *domain.Error
		if domain.IsMissing(err) || errors.As(err, &problem) && problem.Status == 403 {
			next.Inflight = nil
			next.Buffer = nil
			next.Bytes = 0
			next.Due = time.Time{}
		}
	}
	changed := result.Handled || result.Accepted && len(batch.Events) > 0
	if err != nil && next.Inflight != nil {
		s.retry[id] = s.now().Add(time.Second)
	}
	saved := false
	if changed || err != nil {
		state := "pending"
		if next.Inflight == nil && len(next.Buffer) == 0 {
			state = "complete"
		}
		statusUpdates := map[string]endpointState{}
		for _, event := range batch.Events {
			status := s.states[event.EndpointID]
			if result.Accepted {
				status.LastStatus = "delivered"
				status.LastError = ""
			}
			if err != nil {
				status.LastError = err.Error()
				if !result.Accepted {
					status.LastStatus = "failed"
				}
			}
			statusUpdates[event.EndpointID] = status
		}
		saveErr := s.store.Update(ctx, func(tx *persistence.Tx) error {
			if err := tx.SaveMaintenance(ctx, flowKind, id, state, next); err != nil {
				return err
			}
			for endpoint, status := range statusUpdates {
				var active domain.WakeEndpoint
				if err := tx.Get(ctx, Kind, endpoint, &active); domain.IsMissing(err) {
					continue
				} else if err != nil {
					return err
				}
				if err := tx.SaveMaintenance(ctx, stateKind, endpoint, "active", status); err != nil {
					return err
				}
			}
			return nil
		})
		if saveErr == nil {
			saved = true
			s.flows[id] = &next
			for endpoint, status := range statusUpdates {
				s.states[endpoint] = status
			}
			if state == "complete" {
				delete(s.flows, id)
				delete(s.retry, id)
				delete(s.dirty, id)
			}
		} else {
			s.retry[id] = s.now().Add(time.Second)
		}
	}
	s.mu.Unlock()
	if changed && saved {
		s.WakeSession(id)
		s.broadcast()
	}
}

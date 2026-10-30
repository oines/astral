// Package schedule owns durable, Session-targeted prompt delivery in the Host.
package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/timezone"
	"github.com/robfig/cron/v3"
)

const Kind = "schedules"

type Input struct {
	SessionMode string                `json:"session_mode,omitempty" enum:"existing,new"`
	SessionID   string                `json:"session_id,omitempty"`
	Title       string                `json:"title"`
	Prompt      string                `json:"prompt"`
	Enabled     bool                  `json:"enabled"`
	Timing      domain.ScheduleTiming `json:"timing"`
}

// Occurrence freezes the instruction before claiming its delivery. Its ID is
// stable across Host/Core commit boundaries; no command credential is retained.
type Occurrence struct {
	ScheduleID  string    `json:"schedule_id"`
	AgentID     string    `json:"agent_id"`
	SessionMode string    `json:"session_mode,omitempty"`
	SessionID   string    `json:"session_id"`
	Private     bool      `json:"private,omitempty"`
	Title       string    `json:"title"`
	Prompt      string    `json:"prompt"`
	ScheduledAt time.Time `json:"scheduled_at"`
}

func (o Occurrence) InputID() string {
	sum := sha256.Sum256([]byte(o.ScheduleID + ":" + o.ScheduledAt.UTC().Format(time.RFC3339Nano)))
	return "schedule_" + hex.EncodeToString(sum[:])
}

func (o Occurrence) NewSessionID() string {
	// Keep the standard Session ID shape while deriving it from the same
	// durable occurrence identity, rather than generating a new ID on retries.
	return "session_" + strings.TrimPrefix(o.InputID(), "schedule_")[:32]
}

type record struct {
	domain.Schedule
	Pending            *Occurrence `json:"pending_delivery,omitempty"`
	CalculatedTimezone string      `json:"calculated_timezone,omitempty"`
}

type Service struct {
	store       *persistence.Store
	deliver     func(context.Context, Occurrence) error
	accepted    func(context.Context, Occurrence) (bool, error)
	changed     func()
	now         func() time.Time
	timezone    *timezone.Service
	mu          sync.Mutex
	locks       map[string]chan struct{}
	cancel      context.CancelFunc
	done        chan struct{}
	closed      bool
	subscribers map[chan struct{}]struct{}
}

func New(store *persistence.Store, deliver func(context.Context, Occurrence) error, accepted func(context.Context, Occurrence) (bool, error), changed func(), zones ...*timezone.Service) *Service {
	var zone *timezone.Service
	if len(zones) > 0 {
		zone = zones[0]
	}
	if zone == nil {
		zone = timezone.New(store)
	}
	return &Service{store: store, deliver: deliver, accepted: accepted, changed: changed, now: time.Now, timezone: zone, locks: map[string]chan struct{}{}, subscribers: map[chan struct{}]struct{}{}}
}

func (s *Service) notify() {
	s.mu.Lock()
	for subscriber := range s.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
	if s.changed != nil {
		s.changed()
	}
}

// Subscribe is a broadcast invalidation subscription. Register before reading
// the current list, then refresh it after each notification.
func (s *Service) Subscribe() (<-chan struct{}, func()) {
	updates := make(chan struct{}, 1)
	s.mu.Lock()
	s.subscribers[updates] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	return updates, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subscribers, updates)
			s.mu.Unlock()
		})
	}
}

// NotifyChanged also covers plans purged by a resource-deletion transaction.
func (s *Service) NotifyChanged() { s.notify() }

// lock serializes edits and admission for one plan, without holding a global
// lock while opening a Session or admitting an input.
func (s *Service) lock(ctx context.Context, id string) (func(), error) {
	s.mu.Lock()
	lock := s.locks[id]
	if lock == nil {
		lock = make(chan struct{}, 1)
		s.locks[id] = lock
	}
	s.mu.Unlock()
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) List(ctx context.Context, agentID string) ([]domain.Schedule, error) {
	items, err := s.store.List(ctx, Kind)
	if err != nil {
		return nil, err
	}
	result := []domain.Schedule{}
	for _, item := range items {
		var plan domain.Schedule
		if err := json.Unmarshal(item.Data, &plan); err != nil {
			return nil, err
		}
		if agentID == "" || plan.AgentID == agentID {
			if err := authorizePlan(ctx, s.store, plan); domain.IsMissing(err) {
				continue
			} else if err != nil {
				return nil, err
			}
			plan, err = s.present(ctx, plan)
			if err != nil {
				return nil, err
			}
			result = append(result, plan)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *Service) Get(ctx context.Context, agentID, id string) (domain.Schedule, error) {
	var plan domain.Schedule
	if err := s.store.Get(ctx, Kind, id, &plan); err != nil {
		return plan, err
	}
	if agentID != "" && plan.AgentID != agentID {
		return domain.Schedule{}, domain.Missing("Schedule", id)
	}
	if err := authorizePlan(ctx, s.store, plan); err != nil {
		return domain.Schedule{}, err
	}
	return s.present(ctx, plan)
}

func (s *Service) Create(ctx context.Context, agentID string, input Input) (domain.Schedule, error) {
	now := s.now().UTC()
	input, err := normalize(input)
	if err != nil {
		return domain.Schedule{}, err
	}
	location, err := s.timezone.Location(ctx)
	if err != nil {
		return domain.Schedule{}, err
	}
	plan := record{Schedule: domain.Schedule{Meta: domain.Meta{ID: domain.ID("schedule")}, AgentID: agentID}}
	apply(&plan.Schedule, input)
	if scope, ok := persistence.CurrentSessionScope(ctx); ok && input.SessionMode == "new" {
		plan.SourceSessionID = scope.SessionID
	}
	if plan.NextRunAt, err = next(input, now, location); err != nil {
		return domain.Schedule{}, err
	}
	if plan.Timing.Kind == "cron" {
		plan.CalculatedTimezone = location.String()
	}
	err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := target(ctx, tx, agentID, input.SessionMode, input.SessionID); err != nil {
			return err
		}
		if err := authorizePlan(ctx, tx, plan.Schedule); err != nil {
			return err
		}
		return tx.Save(ctx, Kind, &plan.Meta, &plan)
	})
	if err == nil {
		s.notify()
		return s.present(ctx, plan.Schedule)
	}
	return plan.Schedule, err
}

func (s *Service) Update(ctx context.Context, agentID, id string, input Input, revision int64) (domain.Schedule, error) {
	input, err := normalize(input)
	if err != nil {
		return domain.Schedule{}, err
	}
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return domain.Schedule{}, err
	}
	defer unlock()
	location, err := s.timezone.Location(ctx)
	if err != nil {
		return domain.Schedule{}, err
	}
	var plan record
	err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.Get(ctx, Kind, id, &plan); err != nil {
			return err
		}
		if agentID != "" && plan.AgentID != agentID {
			return domain.Missing("Schedule", id)
		}
		if err := authorizePlan(ctx, tx, plan.Schedule); err != nil {
			return err
		}
		if plan.Revision != revision {
			return domain.Conflict("this Schedule changed; reload it before saving")
		}
		if err := target(ctx, tx, plan.AgentID, input.SessionMode, input.SessionID); err != nil {
			return err
		}
		if plan.Enabled != input.Enabled || plan.SessionMode != input.SessionMode || plan.SessionID != input.SessionID || !reflect.DeepEqual(plan.Timing, input.Timing) || (input.Timing.Kind == "cron" && plan.CalculatedTimezone != location.String()) {
			plan.NextRunAt, err = next(input, s.now().UTC(), location)
			if err != nil {
				return err
			}
		}
		apply(&plan.Schedule, input)
		if input.SessionMode != "new" {
			plan.SourceSessionID = ""
		} else if scope, ok := persistence.CurrentSessionScope(ctx); ok && plan.SourceSessionID == "" {
			plan.SourceSessionID = scope.SessionID
		}
		if err := authorizePlan(ctx, tx, plan.Schedule); err != nil {
			return err
		}
		plan.CalculatedTimezone = ""
		if plan.Timing.Kind == "cron" {
			plan.CalculatedTimezone = location.String()
		}
		return tx.Save(ctx, Kind, &plan.Meta, &plan)
	})
	if err == nil {
		s.notify()
		return s.present(ctx, plan.Schedule)
	}
	return plan.Schedule, err
}

func (s *Service) Delete(ctx context.Context, agentID, id string, revision int64) error {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		var plan domain.Schedule
		if err := tx.Get(ctx, Kind, id, &plan); err != nil {
			return err
		}
		if agentID != "" && plan.AgentID != agentID {
			return domain.Missing("Schedule", id)
		}
		if err := authorizePlan(ctx, tx, plan); err != nil {
			return err
		}
		if plan.Revision != revision {
			return domain.Conflict("this Schedule changed; reload it before deleting")
		}
		return tx.Purge(ctx, Kind, id, revision)
	})
	if err == nil {
		s.notify()
	}
	return err
}

func apply(plan *domain.Schedule, input Input) {
	plan.SessionID, plan.Title, plan.Prompt, plan.Enabled, plan.Timing = input.SessionID, input.Title, input.Prompt, input.Enabled, input.Timing
	plan.SessionMode = input.SessionMode
}

type getter interface {
	Get(context.Context, string, string, any) error
}

func authorizePlan(ctx context.Context, store getter, plan domain.Schedule) error {
	if _, scoped := persistence.CurrentSessionScope(ctx); !scoped {
		return nil
	}
	id := plan.SessionID
	if plan.SessionMode == "new" {
		id = plan.SourceSessionID
	}
	if id == "" {
		return nil
	}
	var session domain.Session
	if err := store.Get(ctx, "sessions", id, &session); err != nil {
		return err
	}
	if err := persistence.CheckSessionAccess(ctx, session); err != nil {
		return domain.Missing("Schedule", plan.ID)
	}
	return nil
}

// LastSessionID is a result link, not the plan's authorization scope. Hide an
// unavailable result in command responses without changing the stored plan or
// preventing its management. Owner responses retain the full result link.
func (s *Service) present(ctx context.Context, plan domain.Schedule) (domain.Schedule, error) {
	if _, scoped := persistence.CurrentSessionScope(ctx); !scoped || plan.LastSessionID == "" {
		return plan, nil
	}
	var session domain.Session
	err := s.store.Get(ctx, "sessions", plan.LastSessionID, &session)
	if err == nil {
		err = persistence.CheckSessionAccess(ctx, session)
	}
	if domain.IsMissing(err) {
		plan.LastSessionID = ""
		return plan, nil
	}
	return plan, err
}

func target(ctx context.Context, store getter, agentID, mode, sessionID string) error {
	var agent domain.Agent
	if err := store.Get(ctx, "agents", agentID, &agent); err != nil {
		return err
	}
	if agent.Deletion {
		return domain.Missing("Bot", agentID)
	}
	if mode == "new" {
		return nil
	}
	var session domain.Session
	if err := store.Get(ctx, "sessions", sessionID, &session); err != nil {
		return err
	}
	if session.AgentID != agentID || session.Deletion != nil {
		return domain.Missing("Session", sessionID)
	}
	return persistence.CheckSessionAccess(ctx, session)
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func normalize(input Input) (Input, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Timing.Cron = strings.TrimSpace(input.Timing.Cron)
	if input.SessionMode == "" {
		input.SessionMode = "existing"
	}
	switch input.SessionMode {
	case "existing":
		if input.SessionID == "" {
			return input, domain.Invalid("existing Session mode requires session_id")
		}
	case "new":
		if input.SessionID != "" {
			return input, domain.Invalid("new Session mode cannot specify session_id")
		}
	default:
		return input, domain.Invalid("Session mode must be existing or new")
	}
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 200 {
		return input, domain.Invalid("a Schedule title of at most 200 characters is required")
	}
	if strings.TrimSpace(input.Prompt) == "" || len(input.Prompt) > 1<<20 {
		return input, domain.Invalid("a nonempty Schedule prompt of at most 1 MiB is required")
	}
	switch input.Timing.Kind {
	case "once":
		if input.Timing.At == nil || input.Timing.At.IsZero() || input.Timing.Cron != "" {
			return input, domain.Invalid("a one-time Schedule requires at and cannot specify cron")
		}
		at := input.Timing.At.UTC()
		input.Timing.At = &at
	case "cron":
		if input.Timing.At != nil {
			return input, domain.Invalid("a recurring Schedule cannot specify at")
		}
		if _, err := parse(input.Timing, time.UTC); err != nil {
			return input, err
		}
	default:
		return input, domain.Invalid("Schedule timing kind must be once or cron")
	}
	return input, nil
}

func parse(timing domain.ScheduleTiming, location *time.Location) (cron.Schedule, error) {
	// Timezone prefixes and @every descriptors are not accepted from the user.
	// The Server's global zone is the only scheduling timezone.
	if len(strings.Fields(timing.Cron)) != 5 {
		return nil, domain.Invalid("Schedule cron must contain five fields: minute hour day month weekday")
	}
	value, err := parser.Parse(timing.Cron)
	if err != nil {
		return nil, domain.Invalid("invalid Schedule cron: " + err.Error())
	}
	value.(*cron.SpecSchedule).Location = location
	return value, nil
}

func next(input Input, now time.Time, location *time.Location) (*time.Time, error) {
	if !input.Enabled {
		return nil, nil
	}
	if input.Timing.Kind == "once" {
		if !input.Timing.At.After(now) {
			return nil, domain.Invalid("one-time Schedule must be in the future")
		}
		at := *input.Timing.At
		return &at, nil
	}
	value, err := parse(input.Timing, location)
	if err != nil {
		return nil, err
	}
	at := value.Next(now).UTC()
	if at.IsZero() {
		return nil, domain.Invalid("Schedule cron has no future execution time")
	}
	return &at, nil
}

// PurgeSession is part of the Host's existing permanent-deletion transaction.
func PurgeSession(ctx context.Context, tx *persistence.Tx, sessionID string) error {
	return purge(ctx, tx, func(plan domain.Schedule) bool {
		return plan.SessionID == sessionID || plan.SourceSessionID == sessionID
	})
}

func PurgeAgent(ctx context.Context, tx *persistence.Tx, agentID string) error {
	return purge(ctx, tx, func(plan domain.Schedule) bool { return plan.AgentID == agentID })
}

func purge(ctx context.Context, tx *persistence.Tx, match func(domain.Schedule) bool) error {
	items, err := tx.List(ctx, Kind)
	if err != nil {
		return err
	}
	for _, item := range items {
		var plan domain.Schedule
		if err := json.Unmarshal(item.Data, &plan); err != nil {
			return err
		}
		if match(plan) {
			if err := tx.Purge(ctx, Kind, plan.ID, plan.Revision); err != nil {
				return err
			}
		}
	}
	return nil
}

// Start must be called after Host dependencies are ready. Existing overdue
// times are skipped before any new occurrence can be dispatched.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return domain.Conflict("Schedule service is closed")
	}
	if s.cancel != nil {
		return nil
	}
	if err := s.recover(ctx, s.now().UTC()); err != nil {
		return err
	}
	worker, cancel := context.WithCancel(ctx)
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-worker.Done():
				return
			case <-ticker.C:
				// Transient storage failures retain the due occurrence. A later
				// tick retries admission with the same stable identity.
				if err := s.Step(worker, s.now().UTC()); err != nil && worker.Err() == nil {
					log.Printf("schedule tick: %v", err)
				}
			}
		}
	}()
	return nil
}

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) recover(ctx context.Context, now time.Time) error {
	location, err := s.timezone.Location(ctx)
	if err != nil {
		return err
	}
	items, err := s.store.List(ctx, Kind)
	if err != nil {
		return err
	}
	for _, item := range items {
		var plan record
		if err := json.Unmarshal(item.Data, &plan); err != nil {
			return err
		}
		changed := false
		if plan.Pending != nil {
			if err := s.resolveClaim(ctx, &plan); err != nil {
				return err
			}
			changed = true
		}
		if plan.Enabled && plan.NextRunAt != nil && !plan.NextRunAt.After(now) {
			missed := *plan.NextRunAt
			plan.LastRunAt, plan.LastStatus, plan.LastError = &missed, "missed", ""
			if plan.Timing.Kind == "once" {
				plan.Enabled, plan.NextRunAt = false, nil
			} else {
				plan.NextRunAt, err = next(Input{Enabled: true, Timing: plan.Timing}, now, location)
				if err != nil {
					return err
				}
				plan.CalculatedTimezone = location.String()
			}
			changed = true
		}
		if plan.Timing.Kind == "cron" && plan.CalculatedTimezone != location.String() {
			plan.NextRunAt, err = next(Input{Enabled: plan.Enabled, Timing: plan.Timing}, now, location)
			if err != nil {
				return err
			}
			plan.CalculatedTimezone, changed = location.String(), true
		}
		if changed {
			if err := s.store.Save(ctx, Kind, &plan.Meta, &plan); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) resolveClaim(ctx context.Context, plan *record) error {
	accepted := false
	if s.accepted != nil {
		var err error
		accepted, err = s.accepted(ctx, *plan.Pending)
		if err != nil && !domain.IsMissing(err) {
			return err
		}
	}
	plan.LastStatus, plan.LastError = "missed", "Prompt delivery was interrupted before acceptance; this occurrence will not be retried"
	if accepted {
		plan.LastStatus, plan.LastError = "delivered", ""
	}
	if err := s.rememberSession(ctx, plan); err != nil {
		return err
	}
	plan.Pending = nil
	return nil
}

func (s *Service) rememberSession(ctx context.Context, plan *record) error {
	var session domain.Session
	if err := s.store.Get(ctx, "sessions", plan.Pending.SessionID, &session); domain.IsMissing(err) {
		return nil
	} else if err != nil {
		return err
	}
	if session.AgentID == plan.AgentID && session.Deletion == nil {
		plan.LastSessionID = session.ID
	}
	return nil
}

// Step dispatches currently due occurrences. It is also the deterministic clock
// boundary used by tests. Recurring plans advance beyond now, skipping any
// additional elapsed occurrences instead of accumulating a queue.
func (s *Service) Step(ctx context.Context, now time.Time) error {
	location, err := s.timezone.Location(ctx)
	if err != nil {
		return err
	}
	items, err := s.store.List(ctx, Kind)
	if err != nil {
		return err
	}
	var errs []error
	var pending sync.WaitGroup
	var failures sync.Mutex
	slots := make(chan struct{}, 4)
	addError := func(err error) {
		failures.Lock()
		errs = append(errs, err)
		failures.Unlock()
	}
loop:
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			addError(err)
			break
		}
		var plan record
		if err := json.Unmarshal(item.Data, &plan); err != nil {
			addError(err)
			break
		}
		if plan.Pending != nil || (plan.Timing.Kind == "cron" && plan.CalculatedTimezone != location.String()) || (plan.Enabled && plan.NextRunAt != nil && !plan.NextRunAt.After(now)) {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				addError(ctx.Err())
				break loop
			}
			pending.Add(1)
			go func(id string) {
				defer pending.Done()
				defer func() { <-slots }()
				if err := s.dispatch(ctx, id, now); err != nil {
					addError(fmt.Errorf("schedule %s: %w", id, err))
				}
			}(plan.ID)
		}
	}
	pending.Wait()
	return errors.Join(errs...)
}

func (s *Service) dispatch(ctx context.Context, id string, now time.Time) error {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	var plan record
	if err := s.store.Get(ctx, Kind, id, &plan); domain.IsMissing(err) {
		return nil
	} else if err != nil {
		return err
	}
	if plan.Pending != nil {
		// A prior Core admission committed but the Host result update may
		// have failed. Resolve it without injecting the prompt a second time.
		if err := s.resolveClaim(ctx, &plan); err != nil {
			return err
		}
		if err := s.store.Save(ctx, Kind, &plan.Meta, &plan); err != nil {
			return err
		}
		s.notify()
	}
	claimed, changed := false, false
	err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.Get(ctx, Kind, id, &plan); err != nil {
			return err
		}
		// Read the zone inside the same transaction as the claim. A setting
		// saved before this boundary cannot dispatch an old-zone occurrence.
		var preferences domain.Preferences
		if err := tx.Get(ctx, "preferences", "owner", &preferences); err != nil && !domain.IsMissing(err) {
			return err
		}
		location, err := s.timezone.Resolve(preferences.Timezone)
		if err != nil {
			return err
		}
		if plan.Timing.Kind == "cron" && plan.CalculatedTimezone != location.String() {
			plan.NextRunAt, err = next(Input{Enabled: plan.Enabled, Timing: plan.Timing}, now, location)
			if err != nil {
				return err
			}
			plan.CalculatedTimezone, changed = location.String(), true
			return tx.Save(ctx, Kind, &plan.Meta, &plan)
		}
		if !plan.Enabled || plan.NextRunAt == nil || plan.NextRunAt.After(now) {
			return nil
		}
		if err := target(ctx, tx, plan.AgentID, plan.SessionMode, plan.SessionID); err != nil {
			if !domain.IsMissing(err) {
				return err
			}
			plan.Enabled, plan.NextRunAt, plan.LastStatus, plan.LastError = false, nil, "failed", err.Error()
			changed = true
			return tx.Save(ctx, Kind, &plan.Meta, &plan)
		}
		occurrence := Occurrence{ScheduleID: id, AgentID: plan.AgentID, SessionMode: plan.SessionMode, SessionID: plan.SessionID, Title: plan.Title, Prompt: plan.Prompt, ScheduledAt: *plan.NextRunAt}
		if occurrence.SessionMode == "new" {
			occurrence.SessionID = occurrence.NewSessionID()
			if plan.SourceSessionID != "" {
				var source domain.Session
				if err := tx.Get(ctx, "sessions", plan.SourceSessionID, &source); err != nil {
					return err
				}
				occurrence.Private = source.Private
			}
		}
		plan.Pending, plan.LastRunAt, plan.LastStatus, plan.LastError = &occurrence, &occurrence.ScheduledAt, "dispatching", ""
		claimed, changed = true, true
		if plan.Timing.Kind == "once" {
			plan.Enabled, plan.NextRunAt = false, nil
		} else {
			var err error
			plan.NextRunAt, err = next(Input{Enabled: true, Timing: plan.Timing}, now, location)
			if err != nil {
				return err
			}
		}
		return tx.Save(ctx, Kind, &plan.Meta, &plan)
	})
	if domain.IsMissing(err) {
		return nil // A concurrent permanent Session deletion removed the plan.
	}
	if err != nil {
		return err
	}
	if changed {
		s.notify()
	}
	if !claimed {
		return nil
	}
	var deliveryErr error
	if s.deliver == nil {
		deliveryErr = domain.Offline("Schedule prompt delivery is unavailable")
	} else {
		admission, cancel := context.WithTimeout(ctx, 15*time.Second)
		deliveryErr = s.deliver(admission, *plan.Pending)
		cancel()
	}
	// Preserve the claim on shutdown. Recovery checks Core's durable receipt
	// rather than repeating an instruction whose acceptance may have committed.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if deliveryErr != nil && s.accepted != nil {
		// A transport/context error can race the Core commit. The durable
		// receipt, rather than the return path alone, decides delivery status.
		accepted, checkErr := s.accepted(ctx, *plan.Pending)
		if checkErr != nil {
			return checkErr
		}
		if accepted {
			deliveryErr = nil
		}
	}
	if err := s.rememberSession(ctx, &plan); err != nil {
		return err
	}
	lastSessionID := plan.LastSessionID
	err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.Get(ctx, Kind, id, &plan); err != nil {
			return err
		}
		plan.Pending, plan.LastStatus, plan.LastError = nil, "delivered", ""
		plan.LastSessionID = lastSessionID
		if deliveryErr != nil {
			plan.LastStatus, plan.LastError = "failed", deliveryErr.Error()
		}
		return tx.Save(ctx, Kind, &plan.Meta, &plan)
	})
	if domain.IsMissing(err) {
		return nil
	}
	if err == nil {
		s.notify()
	}
	return err
}

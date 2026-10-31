package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/persistence"
)

type Service struct {
	Store          *persistence.Store
	Harness        *harness.Service
	Registry       Registry
	Client         *http.Client
	gate           sync.Mutex
	bindings       sync.Mutex
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	workers        sync.WaitGroup
	pollers        map[string]poller
	inboxes        map[string]context.CancelFunc
	sending        map[string]bool
	statuses       map[string]Status
	weixinLogins   map[string]*weixinLogin
	probeQuiet     time.Duration
	probeMaxWait   time.Duration
	handoffs       map[string]int64
	agentWorkers   map[string]*agentWorkers
	deletingAgents map[string]bool
	StorageChanged func()
	storageMu      sync.Mutex
	storageWorkMu  sync.Mutex
	storageRunning map[string]bool
}
type poller struct {
	revision int64
	cancel   context.CancelFunc
	done     chan struct{}
}
type Status struct {
	ConnectionID string    `json:"connection_id"`
	State        string    `json:"state"`
	Error        string    `json:"error,omitempty"`
	LastPoll     time.Time `json:"last_poll,omitempty"`
}
type cursor struct {
	domain.Meta
	Value    string `json:"value,omitempty"`
	Sequence int64  `json:"sequence,omitempty"`
}

func New(store *persistence.Store, service *harness.Service) *Service {
	s := &Service{Store: store, Harness: service, Registry: DefaultRegistry(), Client: http.DefaultClient,
		pollers: map[string]poller{}, inboxes: map[string]context.CancelFunc{}, sending: map[string]bool{}, statuses: map[string]Status{}, probeQuiet: 10 * time.Second, probeMaxWait: time.Minute,
		agentWorkers: map[string]*agentWorkers{}, deletingAgents: map[string]bool{}}
	service.IM = s
	return s
}
func decodeRecords[T any](records []persistence.Record) ([]T, error) {
	result := make([]T, 0, len(records))
	for _, record := range records {
		var value T
		if err := json.Unmarshal(record.Data, &value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}
func list[T any](ctx context.Context, store *persistence.Store, kind string) ([]T, error) {
	records, err := store.List(ctx, kind)
	if err != nil {
		return nil, err
	}
	return decodeRecords[T](records)
}
func (s *Service) adapter(ctx context.Context, connection domain.Connection) (Adapter, error) {
	factory := s.Registry[connection.Adapter]
	if factory == nil {
		return nil, domain.Invalid("this channel adapter is not supported")
	}
	token, err := s.Store.Secret(ctx, connection.CredentialID)
	if err != nil {
		return nil, err
	}
	adapter, err := factory(connection, token, s.Client)
	if err == nil {
		// A protocol may need durable per-chat tokens. They stay in the
		// adapter's private records rather than Core or model-visible fields.
		if persistent, ok := adapter.(interface{ setStore(*persistence.Store) }); ok {
			persistent.setStore(s.Store)
		}
	}
	return adapter, err
}
func (s *Service) Capabilities(ctx context.Context, id string) (Capabilities, error) {
	var c domain.Connection
	if err := s.Store.Get(ctx, "connections", id, &c); err != nil {
		return Capabilities{}, err
	}
	adapter, err := s.adapter(ctx, c)
	if err != nil {
		return Capabilities{}, err
	}
	return adapter.Capabilities(), nil
}
func (s *Service) SaveConnection(ctx context.Context, c *domain.Connection, token *string) error {
	return s.saveConnection(ctx, c, token, false)
}
func (s *Service) saveConnection(ctx context.Context, c *domain.Connection, token *string, weixinGrant bool) error {
	if c.Adapter == "weixin.ilink" && !weixinGrant && (c.ID == "" || token != nil) {
		return domain.Invalid("Weixin credentials must be obtained through QR authorization")
	}
	if strings.TrimSpace(c.Name) == "" {
		return domain.Invalid("connection name is required")
	}
	var owner domain.Agent
	if err := s.Store.Get(ctx, "agents", c.AgentID, &owner); err != nil {
		return err
	}
	if c.AccessMode == "" {
		c.AccessMode = "specified"
	}
	if c.AccessMode != "all" && c.AccessMode != "specified" {
		return domain.Invalid("access mode must be all or specified")
	}
	var prior domain.Connection
	if c.ID != "" {
		if err := s.Store.Get(ctx, "connections", c.ID, &prior); err != nil {
			return err
		}
	}
	if prior.ID != "" && prior.Adapter != c.Adapter {
		return domain.Conflict("create a new Connection to change its adapter")
	}
	if prior.ID != "" && prior.AgentID != c.AgentID {
		return domain.Conflict("create a new account to change its Agent")
	}
	credential := ""
	if token != nil {
		credential = *token
	} else if prior.CredentialID != "" {
		var err error
		credential, err = s.Store.Secret(ctx, prior.CredentialID)
		if err != nil {
			return err
		}
	}
	factory := s.Registry[c.Adapter]
	if factory == nil {
		return domain.Invalid("this channel adapter is not supported")
	}
	adapter, err := factory(*c, credential, s.Client)
	if err != nil {
		return err
	}
	identity := Identity{ID: prior.AccountID, Username: prior.Username, Diagnostics: prior.Diagnostics}
	// Metadata and access changes must remain available during a platform
	// outage. New credentials, connection parameters and re-enabling an
	// account still require a verified platform identity.
	if prior.ID == "" || token != nil || !bytes.Equal(prior.Config, c.Config) || c.Enabled && !prior.Enabled {
		identity, err = adapter.Validate(ctx)
		if err != nil {
			return err
		}
	}
	if prior.AccountID != "" && prior.AccountID != identity.ID {
		return domain.Conflict("create a new Connection for a different platform account")
	}
	if c.ID == "" {
		c.ID = domain.ID("connection")
	}
	c.Platform, c.AccountID, c.Username, c.Diagnostics = adapter.Capabilities().Platform, identity.ID, identity.Username, identity.Diagnostics
	c.CredentialID = prior.CredentialID
	c.Cursor = "" // Poll offsets are operational records, not editable configuration.
	s.gate.Lock()
	defer s.gate.Unlock()
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.RequireActiveAgent(ctx, c.AgentID); err != nil {
			return err
		}
		records, err := tx.List(ctx, "connections")
		if err != nil {
			return err
		}
		connections, err := decodeRecords[domain.Connection](records)
		if err != nil {
			return err
		}
		for _, other := range connections {
			if c.Enabled && other.Enabled && c.ID != other.ID && c.Platform == other.Platform && c.AccountID == other.AccountID {
				return domain.Conflict("this platform account already has an enabled Connection")
			}
		}
		if token != nil {
			c.CredentialID, err = tx.PutSecret(ctx, credential)
			if err != nil {
				return err
			}
		}
		return tx.Save(ctx, "connections", &c.Meta, c)
	})
}
func (s *Service) DeleteConnection(ctx context.Context, id string, revision int64) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		records, err := tx.List(ctx, "conversations")
		if err != nil {
			return err
		}
		conversations, err := decodeRecords[domain.Conversation](records)
		if err != nil {
			return err
		}
		for _, conversation := range conversations {
			if conversation.ConnectionID == id {
				return domain.Conflict("disable this Connection to preserve its conversations and receipts")
			}
		}
		return tx.Delete(ctx, "connections", id, revision)
	})
}
func (s *Service) ResolveConversation(ctx context.Context, id, external, topic string) (domain.Conversation, error) {
	var c domain.Connection
	if err := s.Store.Get(ctx, "connections", id, &c); err != nil {
		return domain.Conversation{}, err
	}
	adapter, err := s.adapter(ctx, c)
	if err != nil {
		return domain.Conversation{}, err
	}
	conversation, err := adapter.ResolveConversation(ctx, external, topic)
	if err != nil {
		return conversation, err
	}
	err = s.saveConversation(ctx, &conversation)
	return conversation, err
}
func (s *Service) saveConversation(ctx context.Context, c *domain.Conversation) error {
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var connection domain.Connection
		if err := tx.Get(ctx, "connections", c.ConnectionID, &connection); err != nil {
			return err
		}
		if err := tx.RequireActiveAgent(ctx, connection.AgentID); err != nil {
			return err
		}
		var prior domain.Conversation
		err := tx.Get(ctx, "conversations", c.ID, &prior)
		if err != nil && !domain.IsMissing(err) {
			return err
		}
		if err == nil {
			c.Meta = prior.Meta
			c.ProactiveEnabled = prior.ProactiveEnabled
			if c.Title == prior.Title && c.Kind == prior.Kind {
				return nil
			}
		}
		if err = tx.Save(ctx, "conversations", &c.Meta, c); err != nil {
			return err
		}
		records, err := tx.List(ctx, "sessions")
		if err != nil {
			return err
		}
		sessions, err := decodeRecords[domain.Session](records)
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if session.ConversationID == c.ID && session.BindingID != "" {
				session.Title = c.Title
				if err = tx.Save(ctx, "sessions", &session.Meta, &session); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return domain.Conflict("channel service was already started")
	}
	// A committed dispatch without a receipt may have reached the platform.
	// Recovery never converts that uncertainty into another send.
	records, err := list[domain.Outbox](ctx, s.Store, "outbox")
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Status != "dispatching" {
			continue
		}
		record.Status, record.Error = "unknown", "Server stopped before a platform receipt was committed"
		if err = s.Store.Save(ctx, "outbox", &record.Meta, &record); err != nil {
			return err
		}
	}
	if err = s.recoverCommands(ctx); err != nil {
		return err
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	go s.run()
	return nil
}
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
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
func (s *Service) run() {
	defer close(s.done)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.reconcile()
		select {
		case <-s.ctx.Done():
			s.workers.Wait()
			return
		case <-ticker.C:
		case <-s.Harness.Changes():
		}
	}
}
func (s *Service) reconcile() {
	s.reconcileStorage()
	connections, err := list[domain.Connection](s.ctx, s.Store, "connections")
	if err != nil {
		return
	}
	s.mu.Lock()
	for id, worker := range s.pollers {
		current := slices.IndexFunc(connections, func(c domain.Connection) bool { return c.ID == id && c.Enabled && c.Revision == worker.revision })
		if current < 0 {
			worker.cancel()
			select {
			case <-worker.done:
				delete(s.pollers, id)
			default:
			}
		}
	}
	for _, c := range connections {
		if !c.Enabled || s.deletingAgents[c.AgentID] {
			continue
		}
		if _, exists := s.pollers[c.ID]; exists {
			continue
		}
		group := s.agentWorkersLocked(c.AgentID)
		ctx, cancel := context.WithCancel(group.ctx)
		done := make(chan struct{})
		s.pollers[c.ID] = poller{revision: c.Revision, cancel: cancel, done: done}
		s.workers.Add(1)
		group.workers.Add(1)
		go func() { defer s.workers.Done(); defer group.workers.Done(); defer close(done); s.poll(ctx, c) }()
	}
	s.mu.Unlock()
	bindings, err := list[domain.ConversationBinding](s.ctx, s.Store, "bindings")
	if err != nil {
		return
	}
	s.mu.Lock()
	for _, binding := range bindings {
		if !binding.Enabled || s.deletingAgents[binding.AgentID] {
			continue
		}
		if _, exists := s.inboxes[binding.ID]; exists {
			continue
		}
		group := s.agentWorkersLocked(binding.AgentID)
		ctx, cancel := context.WithCancel(group.ctx)
		s.inboxes[binding.ID] = cancel
		s.workers.Add(1)
		group.workers.Add(1)
		go func() { defer s.workers.Done(); defer group.workers.Done(); s.inbox(ctx, binding) }()
	}
	for id, cancel := range s.inboxes {
		if slices.IndexFunc(bindings, func(b domain.ConversationBinding) bool { return b.ID == id && b.Enabled }) < 0 {
			cancel()
			delete(s.inboxes, id)
		}
	}
	s.mu.Unlock()
	s.dispatchPending()
}
func (s *Service) setStatus(status Status) {
	s.mu.Lock()
	s.statuses[status.ConnectionID] = status
	s.mu.Unlock()
}
func (s *Service) Status(id string) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status, ok := s.statuses[id]; ok {
		return status
	}
	return Status{ConnectionID: id, State: "stopped"}
}
func (s *Service) poll(ctx context.Context, c domain.Connection) {
	adapter, err := s.adapter(ctx, c)
	if err != nil {
		s.setStatus(Status{ConnectionID: c.ID, State: "error", Error: err.Error()})
		return
	}
	offset := cursor{Meta: domain.Meta{ID: c.ID}}
	if err = s.Store.Get(ctx, "channel_cursors", c.ID, &offset); err != nil && !domain.IsMissing(err) {
		s.setStatus(Status{ConnectionID: c.ID, State: "error", Error: err.Error()})
		return
	}
	for ctx.Err() == nil {
		s.setStatus(Status{ConnectionID: c.ID, State: "polling"})
		batch, pollErr := adapter.Poll(ctx, offset.Value)
		if pollErr == nil {
			for _, event := range batch.Events {
				if event.Observation.Reaction != nil {
					var allowed bool
					event, allowed, pollErr = s.routeReaction(ctx, c.ID, event)
					if pollErr != nil {
						break
					}
					if !allowed {
						continue
					}
				}
				if pollErr = s.saveConversation(ctx, &event.Conversation); pollErr != nil {
					break
				}
				if event.Command != nil {
					if pollErr = s.replyToCommand(ctx, adapter, event); pollErr != nil {
						break
					}
					continue
				}
				event.Observation = normalizeSelf(event.Observation)
				if _, pollErr = s.archiveEvent(ctx, event.Observation); pollErr != nil {
					break
				}
				if !event.Observation.Self && event.Observation.Trigger != "" {
					sequence, seqErr := s.Store.ObservationSequence(ctx, event.Observation.ID)
					if seqErr != nil {
						pollErr = seqErr
						break
					}
					var binding domain.ConversationBinding
					if c.AccessMode == "all" {
						binding, pollErr = s.bind(ctx, event.Conversation.ID, "", &sequence)
					} else {
						binding, pollErr = s.activeBinding(ctx, event.Conversation.ID)
					}
					if pollErr != nil {
						break
					}
					if binding.ID != "" {
						pollErr = s.coverProbe(ctx, binding, sequence)
					}
					if pollErr != nil {
						break
					}
				}
			}
			if pollErr == nil && batch.NextCursor != offset.Value {
				next := offset
				next.Value = batch.NextCursor
				pollErr = s.Store.Save(ctx, "channel_cursors", &next.Meta, &next)
				if pollErr == nil {
					offset = next
				}
			}
		}
		if ctx.Err() != nil {
			break
		}
		if pollErr != nil {
			s.setStatus(Status{ConnectionID: c.ID, State: "error", Error: pollErr.Error()})
			if !delay(ctx, 3*time.Second) {
				break
			}
		} else {
			s.setStatus(Status{ConnectionID: c.ID, State: "connected", LastPoll: time.Now().UTC()})
			if len(batch.Events) == 0 && !delay(ctx, 100*time.Millisecond) {
				break
			}
		}
	}
	s.setStatus(Status{ConnectionID: c.ID, State: "stopped"})
}
func delay(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

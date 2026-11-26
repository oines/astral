// Package acp serves Astral's ACP v1 session protocol over authenticated WebSockets.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/persistence"
)

const (
	maxConnections    = 64
	maxPerToken       = 4
	maxLoadedSessions = 16
	maxInFlight       = 16
	maxFrame          = 1 << 20
	maxOutboxBytes    = 4 << 20
	maxOutboxItems    = 128
	writeTimeout      = 5 * time.Second
)

type Service struct {
	harness *harness.Service

	mu          sync.Mutex
	connections map[*connection]struct{}
	closed      bool
}

func New(h *harness.Service) *Service {
	return &Service{harness: h, connections: make(map[*connection]struct{})}
}

// Close cancels upgraded connections, which an HTTP server does not close
// itself on graceful shutdown.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for conn := range s.connections {
		conn.cancel()
	}
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	conn, status := s.connect(r.Context(), r.Header.Get("Authorization"))
	if conn == nil {
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	defer conn.cancel()
	defer s.disconnect(conn)
	w.Header().Set("Acp-Connection-Id", conn.id)
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxFrame)
	conn.ws = ws
	s.touchToken(conn.ctx, conn.tokenID)
	conn.serve()
}

func (s *Service) connect(ctx context.Context, authorization string) (*connection, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, http.StatusServiceUnavailable
	}
	if len(s.connections) >= maxConnections {
		return nil, http.StatusTooManyRequests
	}
	record, err := s.authenticate(ctx, authorization)
	if errors.Is(err, errUnauthorized) {
		return nil, http.StatusUnauthorized
	}
	if domain.IsMissing(err) {
		return nil, http.StatusForbidden
	}
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	count := 0
	for conn := range s.connections {
		if conn.tokenID == record.ID {
			count++
		}
	}
	if count >= maxPerToken {
		return nil, http.StatusTooManyRequests
	}
	ctx, cancel := context.WithCancel(ctx)
	conn := &connection{service: s, id: domain.ID("acp_connection"), botID: record.BotID, tokenID: record.ID, clientName: record.Name, ctx: ctx, cancel: cancel,
		sessions: make(map[string]*loadedSession), outbox: make(chan []byte, maxOutboxItems),
		operations: make(chan struct{}, maxInFlight), cancellations: make(chan struct{}, maxLoadedSessions)}
	s.connections[conn] = struct{}{}
	return conn, 0
}

func (s *Service) disconnect(conn *connection) {
	s.mu.Lock()
	delete(s.connections, conn)
	s.mu.Unlock()
}

func (s *Service) touchToken(ctx context.Context, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.harness.Store.Update(ctx, func(tx *persistence.Tx) error {
		var record tokenRecord
		if err := tx.Get(ctx, tokenKind, id, &record); err != nil {
			return err
		}
		now := time.Now().UTC()
		if record.LastUsedAt != nil && now.Sub(*record.LastUsedAt) < time.Minute {
			return nil
		}
		record.LastUsedAt = &now
		return tx.Save(ctx, tokenKind, &record.Meta, record)
	})
}

type connection struct {
	id, botID, tokenID string
	clientName         string
	service            *Service
	ctx                context.Context
	cancel             context.CancelFunc
	ws                 *websocket.Conn
	outbox             chan []byte
	operations         chan struct{}
	cancellations      chan struct{}
	workers            sync.WaitGroup

	mu          sync.Mutex
	sessions    map[string]*loadedSession
	creating    int
	queuedBytes int
}

func (c *connection) serve() {
	c.workers.Add(2)
	go func() {
		defer c.workers.Done()
		c.writeLoop()
	}()
	go func() {
		defer c.workers.Done()
		c.watchBot()
	}()
	defer func() {
		c.cancel()
		c.service.harness.ReleaseACPMounts(c.id)
		c.workers.Wait()
		// An in-progress load may have finished admission during disconnect.
		c.service.harness.ReleaseACPMounts(c.id)
	}()
	initialized := false
	for {
		kind, raw, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		if kind != websocket.MessageText {
			continue
		}
		request, rpcErr := decodeRequest(raw)
		if rpcErr != nil {
			c.fail(request.ID, rpcErr)
			continue
		}
		if request.Method == "initialize" && !initialized && request.ID != nil {
			initialized = c.initialize(request)
			continue
		}
		if !initialized {
			if request.ID != nil {
				c.fail(request.ID, &rpcError{Code: -32000, Message: "Initialize the ACP connection first."})
			}
			continue
		}
		if request.ID == nil && request.Method != "session/cancel" {
			continue
		}
		permits := c.operations
		if request.Method == "session/cancel" {
			permits = c.cancellations
		}
		select {
		case permits <- struct{}{}:
		default:
			if request.ID != nil {
				c.fail(request.ID, &rpcError{Code: -32000, Message: "Too many pending requests."})
			}
			continue
		}
		work, rpcErr := c.prepare(request)
		if rpcErr != nil {
			<-permits
			if request.ID != nil {
				c.fail(request.ID, rpcErr)
			}
			continue
		}
		c.workers.Add(1)
		go func() {
			defer c.workers.Done()
			defer func() { <-permits }()
			if err := work(); err != nil && request.ID != nil {
				c.fail(request.ID, err)
			}
		}()
	}
}

func (c *connection) watchBot() {
	watch := c.service.harness.Store.Watch(persistence.ChangeKey{Kind: "agents", ID: c.botID})
	defer watch.Close()
	for {
		changed := watch.Changes()
		if availableBot(c.ctx, c.service.harness.Store, c.botID) != nil {
			c.cancel()
			return
		}
		select {
		case <-c.ctx.Done():
			return
		case <-changed:
		}
	}
}

func (c *connection) writeLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case raw := <-c.outbox:
			ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, raw)
			cancel()
			c.mu.Lock()
			c.queuedBytes -= len(raw)
			c.mu.Unlock()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (c *connection) send(value any, replay bool) bool {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxFrame || c.ctx.Err() != nil {
		c.cancel()
		return false
	}
	c.mu.Lock()
	if c.queuedBytes+len(raw) > maxOutboxBytes {
		c.mu.Unlock()
		c.cancel()
		return false
	}
	c.queuedBytes += len(raw)
	c.mu.Unlock()
	if replay {
		select {
		case c.outbox <- raw:
			return true
		case <-c.ctx.Done():
			return false
		}
	}
	select {
	case c.outbox <- raw:
		return true
	default:
		c.cancel()
		return false
	}
}

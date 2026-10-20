// Package computers implements authenticated outbound Daemon connections.
package computers

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/transport"
)

type GatewayHandler func(context.Context, GatewayRequest) (GatewayResponse, error)

type connection struct {
	peer      *transport.Peer
	hello     Hello
	connected time.Time
}
type Hub struct {
	Store   *persistence.Store
	Gateway GatewayHandler
	mu      sync.RWMutex
	live    map[string]*connection
	grants  map[string]domain.ExecutionBinding
}

func NewHub(store *persistence.Store) *Hub {
	return &Hub{Store: store, live: map[string]*connection{}, grants: map[string]domain.ExecutionBinding{}}
}

func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (h *Hub) Enroll(ctx context.Context, serverURL string, container, local bool) (EnrollmentResult, error) {
	if err := modelbridge.ValidateURL(serverURL); err != nil {
		return EnrollmentResult{}, err
	}
	token := domain.ID("enroll")
	e := domain.Enrollment{Meta: domain.Meta{ID: domain.ID("enrollment")}, TokenHash: tokenHash(token), ExpiresAt: time.Now().UTC().Add(15 * time.Minute), Container: container, Local: local, State: "waiting"}
	if err := h.Store.Save(ctx, "enrollments", &e.Meta, &e); err != nil {
		return EnrollmentResult{}, err
	}
	// The installer command is completed by the distribution layer; this command
	// works with both the packaged executable and a locally built CLI.
	cmd := "astral computer connect --server " + shellQuote(serverURL) + " --enrollment " + shellQuote(e.ID) + " --token " + shellQuote(token)
	if container {
		cmd += " --container"
	}
	return EnrollmentResult{Enrollment: e, Token: token, Command: cmd}, nil
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (h *Hub) Register(ctx context.Context, id string, request EnrollmentRequest) (Credentials, error) {
	var credentials Credentials
	err := h.Store.Update(ctx, func(tx *persistence.Tx) error {
		var e domain.Enrollment
		if err := tx.Get(ctx, "enrollments", id, &e); err != nil {
			return domain.Forbidden("invalid enrollment")
		}
		hash := tokenHash(request.Token)
		if subtle.ConstantTimeCompare([]byte(e.TokenHash), []byte(hash)) != 1 || time.Now().After(e.ExpiresAt) {
			return domain.Forbidden("enrollment expired or invalid")
		}
		if e.State == "registered" {
			credentials.ComputerID = e.ComputerID
			return nil
		}
		if e.State != "waiting" {
			return domain.Conflict("enrollment is no longer available")
		}
		if err := validateHello(request.Hello); err != nil {
			return err
		}
		token := domain.ID("computer_token")
		credential, err := tx.PutSecret(ctx, token)
		if err != nil {
			return err
		}
		kind := "remote"
		if e.Local {
			kind = "local"
		}
		if e.Container {
			kind = "container"
		}
		c := domain.Computer{Meta: domain.Meta{ID: domain.ID("computer")}, Name: request.Hello.Hostname, Hostname: request.Hello.Hostname,
			Kind: kind, Local: e.Local && !e.Container, OS: request.Hello.OS, Arch: request.Hello.Arch, Shell: request.Hello.Shell, Home: request.Hello.Home, TokenID: credential}
		if err = tx.Save(ctx, "computers", &c.Meta, &c); err != nil {
			return err
		}
		e.State, e.ComputerID, e.CredentialID = "registered", c.ID, credential
		credentials.ComputerID = c.ID
		return tx.Save(ctx, "enrollments", &e.Meta, &e)
	})
	if err != nil {
		return credentials, err
	}
	// Read outside the transaction: the SQLite store uses one writer connection.
	var c domain.Computer
	if err = h.Store.Get(ctx, "computers", credentials.ComputerID, &c); err != nil {
		return credentials, err
	}
	credentials.Token, err = h.Store.Secret(ctx, c.TokenID)
	return credentials, err
}

func validateHello(hello Hello) error {
	if hello.BootID == "" || len(hello.BootID) > 100 || hello.Hostname == "" || len(hello.Hostname) > 250 ||
		(hello.OS != "darwin" && hello.OS != "linux") || !strings.HasPrefix(hello.Home, "/") || !strings.HasPrefix(hello.Shell, "/") {
		return domain.Invalid("daemon reported an unsupported or incomplete environment")
	}
	return nil
}

func (h *Hub) Connect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c domain.Computer
	if err := h.Store.Get(r.Context(), "computers", id, &c); err != nil || c.Revoked {
		http.Error(w, "computer is unavailable", http.StatusForbidden)
		return
	}
	secret, err := h.Store.Secret(r.Context(), c.TokenID)
	if err != nil || secret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
		http.Error(w, "invalid computer credential", http.StatusUnauthorized)
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Astral-Hello"))
	var hello Hello
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &hello) != nil || validateHello(hello) != nil {
		http.Error(w, "invalid daemon environment", http.StatusBadRequest)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	active := &connection{hello: hello, connected: time.Now().UTC()}
	ready := make(chan struct{})
	active.peer = transport.New(conn, func(ctx context.Context, request transport.Request, out *transport.Responder) error {
		<-ready
		if request.Method != "gateway" || h.Gateway == nil {
			return domain.Missing("daemon method", request.Method)
		}
		var call GatewayRequest
		if err := json.Unmarshal(request.Params, &call); err != nil {
			return domain.Invalid("invalid gateway request")
		}
		if call.Binding.ComputerID != id || call.Binding.BootID != hello.BootID {
			return domain.Forbidden("gateway source does not match this connection")
		}
		if err := h.Validate(ctx, call.Binding); err != nil {
			return err
		}
		result, err := h.Gateway(ctx, call)
		if err != nil {
			return err
		}
		return out.JSON(result)
	})
	h.mu.Lock()
	old := h.live[id]
	h.live[id] = active
	h.mu.Unlock()
	close(ready)
	if old != nil {
		_ = old.peer.Close()
	}
	<-active.peer.Done()
	h.mu.Lock()
	if h.live[id] == active {
		delete(h.live, id)
	}
	h.mu.Unlock()
}

func (h *Hub) Status(c domain.Computer) domain.ComputerStatus {
	status := domain.ComputerStatus{Computer: c}
	h.mu.RLock()
	active := h.live[c.ID]
	h.mu.RUnlock()
	if active != nil && !c.Revoked {
		status.Online, status.BootID = true, active.hello.BootID
		status.ConnectedAt = &active.connected
		status.Hostname, status.OS, status.Arch, status.Shell, status.Home = active.hello.Hostname, active.hello.OS, active.hello.Arch, active.hello.Shell, active.hello.Home
	}
	return status
}

func (h *Hub) Bind(ctx context.Context, agentID, sessionID, computerID, cwd string) (domain.ExecutionBinding, error) {
	b := domain.ExecutionBinding{GrantID: domain.ID("grant"), AgentID: agentID, SessionID: sessionID, ComputerID: computerID, CWD: cwd}
	if err := h.authorize(ctx, b); err != nil {
		return b, err
	}
	h.mu.RLock()
	active := h.live[computerID]
	if active != nil {
		for _, existing := range h.grants {
			if existing.AgentID == agentID && existing.SessionID == sessionID && existing.ComputerID == computerID &&
				existing.BootID == active.hello.BootID && (existing.CWD == cwd || cwd == "" && existing.CWD == existing.AgentHome) {
				h.mu.RUnlock()
				return existing, nil
			}
		}
	}
	h.mu.RUnlock()
	if active == nil {
		return b, domain.Offline("computer is offline")
	}
	b.BootID, b.Shell = active.hello.BootID, active.hello.Shell
	var prepared domain.ExecutionBinding
	if err := active.peer.JSON(ctx, "environment", Call{Binding: b}, &prepared); err != nil {
		return b, err
	}
	// Only the target's absolute paths are supplied by the Daemon.
	if !strings.HasPrefix(prepared.CWD, "/") || !strings.HasPrefix(prepared.AgentHome, "/") {
		return b, domain.Invalid("daemon returned invalid directories")
	}
	b.CWD, b.AgentHome = prepared.CWD, prepared.AgentHome
	h.mu.Lock()
	if h.live[computerID] != active {
		h.mu.Unlock()
		return b, domain.Offline("computer reconnected while preparing its environment")
	}
	h.grants[b.GrantID] = b
	h.mu.Unlock()
	return b, nil
}
func (h *Hub) authorize(ctx context.Context, b domain.ExecutionBinding) error {
	var agent domain.Agent
	if err := h.Store.Get(ctx, "agents", b.AgentID, &agent); err != nil {
		return err
	}
	if !slices.Contains(agent.AllowedComputers, b.ComputerID) {
		return domain.Forbidden("computer is not allowed for this agent")
	}
	var session domain.Session
	if err := h.Store.Get(ctx, "sessions", b.SessionID, &session); err != nil {
		return err
	}
	if session.AgentID != b.AgentID {
		return domain.Forbidden("session does not belong to this agent")
	}
	var computer domain.Computer
	if err := h.Store.Get(ctx, "computers", b.ComputerID, &computer); err != nil {
		return err
	}
	if computer.Revoked {
		return domain.Forbidden("computer access was revoked")
	}
	return nil
}
func (h *Hub) Validate(ctx context.Context, b domain.ExecutionBinding) error {
	h.mu.RLock()
	grant, exists := h.grants[b.GrantID]
	active := h.live[b.ComputerID]
	h.mu.RUnlock()
	if !exists || grant != b {
		return domain.Forbidden("execution binding is no longer valid")
	}
	if active == nil || active.hello.BootID != b.BootID {
		return domain.Offline("the original computer process is no longer connected")
	}
	return h.authorize(ctx, b)
}
func (h *Hub) Peer(ctx context.Context, b domain.ExecutionBinding) (*transport.Peer, error) {
	if err := h.Validate(ctx, b); err != nil {
		return nil, err
	}
	h.mu.RLock()
	active := h.live[b.ComputerID]
	h.mu.RUnlock()
	if active == nil || active.hello.BootID != b.BootID {
		return nil, domain.Offline("the original computer connection is unavailable")
	}
	return active.peer, nil
}
func (h *Hub) Call(ctx context.Context, method string, call Call, body io.Reader) (json.RawMessage, io.ReadCloser, error) {
	p, err := h.Peer(ctx, call.Binding)
	if err != nil {
		return nil, nil, err
	}
	return p.Call(ctx, method, call, body)
}
func (h *Hub) JSON(ctx context.Context, method string, call Call, result any) error {
	p, err := h.Peer(ctx, call.Binding)
	if err != nil {
		return err
	}
	return p.JSON(ctx, method, call, result)
}
func (h *Hub) Revoke(ctx context.Context, id string) error {
	var c domain.Computer
	if err := h.Store.Get(ctx, "computers", id, &c); err != nil {
		return err
	}
	c.Revoked = true
	if err := h.Store.Save(ctx, "computers", &c.Meta, &c); err != nil {
		return err
	}
	h.mu.RLock()
	active := h.live[id]
	h.mu.RUnlock()
	if active != nil {
		_ = active.peer.Close()
	}
	return nil
}
func (h *Hub) Close() error {
	h.mu.Lock()
	peers := make([]*transport.Peer, 0, len(h.live))
	for _, active := range h.live {
		peers = append(peers, active.peer)
	}
	clear(h.grants)
	h.mu.Unlock()
	var errs []error
	for _, peer := range peers {
		errs = append(errs, peer.Close())
	}
	return errors.Join(errs...)
}

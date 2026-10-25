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
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/serviceurl"
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
	if err := serviceurl.Validate(serverURL); err != nil {
		return EnrollmentResult{}, err
	}
	token := domain.ID("enroll")
	e := domain.Enrollment{Meta: domain.Meta{ID: domain.ID("enrollment")}, TokenHash: tokenHash(token), ExpiresAt: time.Now().UTC().Add(15 * time.Minute), Container: container, Local: local, State: "waiting"}
	if err := h.Store.Save(ctx, "enrollments", &e.Meta, &e); err != nil {
		return EnrollmentResult{}, err
	}
	cmd := "curl --fail --silent --show-error " + shellQuote(strings.TrimRight(serverURL, "/")+"/install.sh") +
		" | sh -s -- --server " + shellQuote(serverURL) + " --enrollment " + shellQuote(e.ID) + " --token " + shellQuote(token)
	ps := "& ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing " + powerShellQuote(strings.TrimRight(serverURL, "/")+"/install.ps1") + ").Content)) -Server " + powerShellQuote(serverURL) + " -Enrollment " + powerShellQuote(e.ID) + " -Token " + powerShellQuote(token)
	if container {
		cmd += " --container"
		ps += " -Container"
	}
	return EnrollmentResult{Enrollment: e, Token: token, Command: cmd, PowerShellCommand: ps}, nil
}
func shellQuote(value string) string      { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
func powerShellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

// PrepareContainer claims the normal short-lived enrollment before expensive
// image setup. Retries cannot extend it beyond the fixed creation-time limit.
func (h *Hub) PrepareContainer(ctx context.Context, id, token string) error {
	return h.Store.Update(ctx, func(tx *persistence.Tx) error {
		var enrollment domain.Enrollment
		if err := tx.Get(ctx, "enrollments", id, &enrollment); err != nil {
			return domain.Forbidden("invalid container enrollment")
		}
		hash := tokenHash(token)
		if token == "" || !enrollment.Container || enrollment.State != "waiting" || time.Now().After(enrollment.ExpiresAt) || subtle.ConstantTimeCompare([]byte(enrollment.TokenHash), []byte(hash)) != 1 {
			return domain.Forbidden("container enrollment expired or invalid")
		}
		deadline := enrollment.CreatedAt.Add(90 * time.Minute)
		if !enrollment.ExpiresAt.Before(deadline) {
			return nil
		}
		enrollment.ExpiresAt = deadline
		return tx.Save(ctx, "enrollments", &enrollment.Meta, &enrollment)
	})
}

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
		applyEnvironment(&c, request.Hello)
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
		(hello.OS != "darwin" && hello.OS != "linux" && hello.OS != "windows") || !absoluteTargetPath(hello.OS, hello.Home) || !absoluteTargetPath(hello.OS, hello.Shell) {
		return domain.Invalid("daemon reported an unsupported or incomplete environment")
	}
	if hello.Root != "" && (!absoluteTargetPath(hello.OS, hello.Root) || len(hello.Root) > 1024) {
		return domain.Invalid("daemon reported an invalid data directory")
	}
	if c := hello.Container; c != nil {
		if c.Name == "" || len(c.Name) > 250 || len(c.Host) > 250 || len(c.Image) > 512 || len(c.Volume) > 250 ||
			c.MountPath != "" && (!strings.HasPrefix(c.MountPath, "/") || len(c.MountPath) > 1024) {
			return domain.Invalid("daemon reported invalid container information")
		}
	}
	return nil
}

// Optional fields survive older Daemons reconnecting without them. The display
// name and Computer kind remain owned by the Server, not by reported metadata.
func applyEnvironment(c *domain.Computer, hello Hello) bool {
	previous := *c
	if hello.Version != "" {
		c.DaemonVersion = hello.Version
	}
	c.Hostname, c.OS, c.Arch, c.Shell, c.Home = hello.Hostname, hello.OS, hello.Arch, hello.Shell, hello.Home
	if hello.Root != "" {
		c.Root = hello.Root
	}
	if c.Kind == "container" && hello.Container != nil {
		value := *hello.Container
		c.Container = &value
	}
	sameContainer := c.Container == previous.Container || c.Container != nil && previous.Container != nil && *c.Container == *previous.Container
	return c.Hostname != previous.Hostname || c.OS != previous.OS || c.Arch != previous.Arch || c.Shell != previous.Shell || c.Home != previous.Home || c.Root != previous.Root || c.DaemonVersion != previous.DaemonVersion || !sameContainer
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
	// Persist the latest reported environment so the same information remains
	// available offline. Read again in the transaction to preserve concurrent edits.
	if err = h.Store.Update(r.Context(), func(tx *persistence.Tx) error {
		if err := tx.Get(r.Context(), "computers", id, &c); err != nil {
			return err
		}
		if c.Revoked {
			return domain.Forbidden("computer is unavailable")
		}
		if !applyEnvironment(&c, hello) {
			return nil
		}
		return tx.Save(r.Context(), "computers", &c.Meta, &c)
	}); err != nil {
		http.Error(w, "computer environment could not be saved", http.StatusServiceUnavailable)
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
		if result.Stream != nil {
			defer result.Stream.Close()
		}
		if err := out.Headers(result); err != nil {
			return err
		}
		if result.Stream != nil {
			_, err = io.Copy(out, result.Stream)
		} else {
			_, err = out.Write(result.Body)
		}
		return err
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
		applyEnvironment(&status.Computer, active.hello)
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
	// The Daemon supplies its absolute paths and this binding's local API URL.
	if !absoluteTargetPath(active.hello.OS, prepared.CWD) || !absoluteTargetPath(active.hello.OS, prepared.AgentHome) {
		return b, domain.Invalid("daemon returned invalid directories")
	}
	b.CWD, b.AgentHome, b.APIURL = prepared.CWD, prepared.AgentHome, prepared.APIURL
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
	if session.Deletion != nil {
		return domain.Missing("Session", b.SessionID)
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

// Forget releases Server-side connection and execution grant state after a
// registration has been deleted. It does not manage the remote Daemon process.
func (h *Hub) Forget(id string) {
	h.mu.Lock()
	active := h.live[id]
	delete(h.live, id)
	for key, grant := range h.grants {
		if grant.ComputerID == id {
			delete(h.grants, key)
		}
	}
	h.mu.Unlock()
	if active != nil {
		_ = active.peer.Close()
	}
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

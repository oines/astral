// Package computers implements authenticated outbound Daemon connections.
package computers

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/securetransport"
	"github.com/oines/astral/internal/serviceurl"
	"github.com/oines/astral/internal/transport"
)

type GatewayHandler func(context.Context, GatewayRequest) (GatewayResponse, error)

type connection struct {
	peer      *transport.Peer
	hello     Hello
	connected time.Time
}
type authenticationBucket struct {
	tokens  float64
	updated time.Time
}

type Hub struct {
	Store              *persistence.Store
	Gateway            GatewayHandler
	ContainerURL       func(*http.Request, string) string
	identity           *securetransport.Identity
	pending            chan struct{}
	authenticating     map[string]int
	attempts           map[string]authenticationBucket
	mu                 sync.RWMutex
	live               map[string]*connection
	grants             map[string]domain.ExecutionBinding
	commandCredentials map[string]*commandCredential
}

func NewHub(store *persistence.Store) *Hub {
	return &Hub{Store: store, live: map[string]*connection{}, grants: map[string]domain.ExecutionBinding{}, commandCredentials: map[string]*commandCredential{}, pending: make(chan struct{}, 64), authenticating: map[string]int{}, attempts: map[string]authenticationBucket{}}
}

func (h *Hub) SetIdentity(identity *securetransport.Identity) { h.identity = identity }
func (h *Hub) ServerPin() string {
	if h.identity == nil {
		return ""
	}
	return h.identity.Pin()
}

func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (h *Hub) Enroll(ctx context.Context, serverURL string, container, local bool) (EnrollmentResult, error) {
	if h.identity == nil {
		return EnrollmentResult{}, errors.New("computer identity for this Server is not configured")
	}
	if err := serviceurl.Validate(serverURL); err != nil {
		return EnrollmentResult{}, err
	}
	token := domain.ID("enroll")
	e := domain.Enrollment{Meta: domain.Meta{ID: domain.ID("enrollment")}, TokenHash: tokenHash(token), ExpiresAt: time.Now().UTC().Add(15 * time.Minute), Container: container, Local: local, State: "waiting"}
	if err := h.Store.Save(ctx, "enrollments", &e.Meta, &e); err != nil {
		return EnrollmentResult{}, err
	}
	cmd := "--server " + shellQuote(serverURL) + " --server-pin " + shellQuote(h.ServerPin()) + " --enrollment " + shellQuote(e.ID) + " --token " + shellQuote(token)
	ps := "-Server " + powerShellQuote(serverURL) + " -ServerPin " + powerShellQuote(h.ServerPin()) + " -Enrollment " + powerShellQuote(e.ID) + " -Token " + powerShellQuote(token)

	if container {
		cmd += " --container"
		ps += " -Container"
	}
	return EnrollmentResult{Enrollment: e, Token: token, ServerPin: h.ServerPin(), Command: ShellInstallerCommand(cmd), PowerShellCommand: PowerShellInstallerCommand(ps)}, nil
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
		e.State, e.ComputerID, e.CredentialID, e.TokenHash = "registered", c.ID, credential, ""
		credentials.ComputerID, credentials.Token, credentials.ServerPin = c.ID, token, h.ServerPin()
		return tx.Save(ctx, "enrollments", &e.Meta, &e)
	})
	if err != nil {
		return credentials, err
	}
	return credentials, nil
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

// Connect is the only network entry for enrollment and Computer authentication.
// No secret or Computer metadata is accepted in HTTP headers or query strings.
func (h *Hub) Connect(w http.ResponseWriter, r *http.Request) {
	if h.identity == nil {
		http.Error(w, "computer identity is unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Origin") != "" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Astral-Hello") != "" {
		http.Error(w, "invalid computer tunnel request", http.StatusForbidden)
		return
	}
	source, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "invalid peer", 400)
		return
	}
	h.mu.Lock()
	now := time.Now()
	bucket, known := h.attempts[source]
	if !known && len(h.attempts) >= 4096 {
		for key, previous := range h.attempts {
			if now.Sub(previous.updated) > 2*time.Minute {
				delete(h.attempts, key)
			}
		}
	}
	if !known && len(h.attempts) >= 4096 {
		h.mu.Unlock()
		http.Error(w, "computer authentication is busy", http.StatusTooManyRequests)
		return
	}
	if !known {
		bucket = authenticationBucket{tokens: 64, updated: now}
	}
	bucket.tokens = min(64, bucket.tokens+now.Sub(bucket.updated).Seconds()*16)
	allowed := bucket.tokens >= 1
	if allowed {
		bucket.tokens--
	}
	bucket.updated = now
	h.attempts[source] = bucket
	h.mu.Unlock()
	if !allowed {
		http.Error(w, "too many computer authentication attempts", http.StatusTooManyRequests)
		return
	}
	select {
	case h.pending <- struct{}{}:
	default:
		http.Error(w, "computer authentication is busy", http.StatusTooManyRequests)
		return
	}
	h.mu.Lock()
	h.authenticating[source]++
	count := h.authenticating[source]
	h.mu.Unlock()
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		<-h.pending
		h.mu.Lock()
		h.authenticating[source]--
		if h.authenticating[source] == 0 {
			delete(h.authenticating, source)
		}
		h.mu.Unlock()
	}
	defer release()
	if count > 16 {
		http.Error(w, "too many computer authentication attempts", http.StatusTooManyRequests)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	raw := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
	ws.SetReadLimit(64 << 10)
	defer raw.Close()
	conn := tls.Server(raw, h.identity.ServerConfig())
	handshakeCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	err = conn.HandshakeContext(handshakeCtx)
	cancel()
	if err != nil {
		return
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	var request tunnelRequest
	if readAuthentication(conn, &request) != nil {
		return
	}
	response := tunnelResponse{}
	switch request.Operation {
	case "register":
		credentials, err := h.Register(r.Context(), request.EnrollmentID, EnrollmentRequest{Token: request.Token, Hello: request.Hello})
		if err != nil {
			response.Error = err.Error()
		} else {
			response.Credentials = &credentials
		}
	case "prepare":
		err := h.PrepareContainer(r.Context(), request.EnrollmentID, request.Token)
		if err != nil {
			response.Error = err.Error()
		} else if h.ContainerURL != nil {
			response.LocalContainerURL = h.ContainerURL(r, request.Hostname)
		}
	case "status", "connect":
		computer, err := h.authenticateComputer(r.Context(), request.ComputerID, request.Token)
		if err != nil {
			response.Error = "invalid Computer credential"
		} else if request.Operation == "status" {
			status := h.Status(computer)
			status.TokenID = ""
			response.Status = &status
		} else if err = validateHello(request.Hello); err != nil {
			response.Error = err.Error()
		}
	default:
		response.Error = "unsupported Computer authentication operation"
	}
	if request.Operation == "connect" && response.Error == "" {
		if err = h.saveEnvironment(r.Context(), request.ComputerID, request.Hello); err != nil {
			response.Error = "Computer is unavailable"
		}
	}
	if writeAuthentication(conn, response) != nil || response.Error != "" || request.Operation != "connect" {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	release()
	h.serveConnection(r.Context(), conn, request.ComputerID, request.Hello)
}

func (h *Hub) authenticateComputer(ctx context.Context, id, token string) (domain.Computer, error) {
	var computer domain.Computer
	if token == "" || h.Store.Get(ctx, "computers", id, &computer) != nil || computer.Revoked {
		return computer, domain.Forbidden("invalid Computer credential")
	}
	secret, err := h.Store.Secret(ctx, computer.TokenID)
	if err != nil || secret == "" || subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
		return computer, domain.Forbidden("invalid Computer credential")
	}
	return computer, nil
}
func (h *Hub) saveEnvironment(ctx context.Context, id string, hello Hello) error {
	return h.Store.Update(ctx, func(tx *persistence.Tx) error {
		var computer domain.Computer
		if err := tx.Get(ctx, "computers", id, &computer); err != nil {
			return err
		}
		if computer.Revoked {
			return domain.Forbidden("Computer is revoked")
		}
		if !applyEnvironment(&computer, hello) {
			return nil
		}
		return tx.Save(ctx, "computers", &computer.Meta, &computer)
	})
}
func (h *Hub) serveConnection(ctx context.Context, conn net.Conn, id string, hello Hello) {
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
		ctx, call, release, err := h.commandRequest(ctx, active, call)
		if err != nil {
			return err
		}
		defer release()
		stopResponse := context.AfterFunc(ctx, func() { out.Abort(context.Cause(ctx)) })
		defer stopResponse()
		result, err := h.Gateway(ctx, call)
		if err != nil {
			return err
		}
		if result.Stream != nil {
			stopStream := context.AfterFunc(ctx, func() { _ = result.Stream.Close() })
			defer stopStream()
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
	var current domain.Computer
	if err := h.Store.Get(ctx, "computers", id, &current); err != nil || current.Revoked {
		h.mu.Unlock()
		close(ready)
		_ = active.peer.Close()
		return
	}
	old := h.live[id]
	h.live[id] = active
	h.mu.Unlock()
	close(ready)
	if old != nil {
		h.revokeConnectionCredentials(old)
		_ = old.peer.Close()
	}
	<-active.peer.Done()
	h.revokeConnectionCredentials(active)
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
		h.revokeConnectionCredentials(active)
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
		h.revokeConnectionCredentials(active)
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
	h.revokeConnectionCredentials(nil)
	var errs []error
	for _, peer := range peers {
		errs = append(errs, peer.Close())
	}
	return errors.Join(errs...)
}

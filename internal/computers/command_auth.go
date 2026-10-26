package computers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"slices"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/transport"
)

// commandCredential is memory-only. It never enters an execution binding,
// persisted process handle, environment snapshot or model request.
type commandCredential struct {
	hash       string
	execution  string
	binding    domain.ExecutionBinding
	connection *connection
	ctx        context.Context
	cancel     context.CancelFunc
}

func unauthorizedCommand() error {
	return &transport.Error{Code: "unauthorized", Message: "a live command API credential is required", Outcome: "not_executed"}
}

func (h *Hub) issueCommandCredential(ctx context.Context, binding domain.ExecutionBinding, execution string, peer *transport.Peer) (string, *commandCredential, error) {
	if execution == "" {
		return "", nil, domain.Invalid("process execution identity is required")
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	h.mu.Lock()
	defer h.mu.Unlock()
	active := h.live[binding.ComputerID]
	if active == nil || active.peer != peer || active.hello.BootID != binding.BootID {
		return "", nil, domain.Offline("the original computer connection is unavailable")
	}
	select {
	case <-peer.Done():
		return "", nil, domain.Offline("the original computer connection is unavailable")
	default:
	}
	if grant, exists := h.grants[binding.GrantID]; !exists || grant != binding {
		return "", nil, domain.Forbidden("execution binding is no longer valid")
	}
	if err := h.authorize(ctx, binding); err != nil {
		return "", nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	credential := &commandCredential{hash: tokenHash(token), execution: execution, binding: binding, connection: active, ctx: lifetime, cancel: cancel}
	h.commandCredentials[credential.hash] = credential
	return token, credential, nil
}

func (h *Hub) revokeCommandCredential(credential *commandCredential) {
	if credential == nil {
		return
	}
	h.mu.Lock()
	if h.commandCredentials[credential.hash] == credential {
		delete(h.commandCredentials, credential.hash)
	}
	h.mu.Unlock()
	credential.cancel()
}

func (h *Hub) revokeConnectionCredentials(active *connection) {
	h.mu.Lock()
	var revoked []*commandCredential
	for hash, credential := range h.commandCredentials {
		if active == nil || credential.connection == active {
			delete(h.commandCredentials, hash)
			revoked = append(revoked, credential)
		}
	}
	h.mu.Unlock()
	for _, credential := range revoked {
		credential.cancel()
	}
}

// ReconcileAgentCommands invalidates credentials and their in-flight API
// requests after a Bot's computer permissions change. Read the saved state
// under the issuance lock so an older save cannot revoke a newer permission.
func (h *Hub) ReconcileAgentCommands(ctx context.Context, agentID string) {
	h.mu.Lock()
	var agent domain.Agent
	err := h.Store.Get(ctx, "agents", agentID, &agent)
	var revoked []*commandCredential
	for hash, credential := range h.commandCredentials {
		if credential.binding.AgentID == agentID && (err != nil || agent.Deletion || !slices.Contains(agent.AllowedComputers, credential.binding.ComputerID)) {
			delete(h.commandCredentials, hash)
			revoked = append(revoked, credential)
		}
	}
	h.mu.Unlock()
	for _, credential := range revoked {
		credential.cancel()
	}
}

// commandRequest derives authority from the Server's credential record. The
// Daemon's listener binding must agree, but cannot select another identity.
func (h *Hub) commandRequest(ctx context.Context, active *connection, call GatewayRequest) (context.Context, GatewayRequest, func(), error) {
	if !validAPIToken(call.APIToken) {
		return nil, GatewayRequest{}, nil, unauthorizedCommand()
	}
	h.mu.RLock()
	credential := h.commandCredentials[tokenHash(call.APIToken)]
	valid := credential != nil && credential.connection == active &&
		h.live[credential.binding.ComputerID] == active &&
		credential.binding == call.Binding && credential.ctx.Err() == nil
	h.mu.RUnlock()
	if !valid {
		return nil, GatewayRequest{}, nil, unauthorizedCommand()
	}
	select {
	case <-active.peer.Done():
		return nil, GatewayRequest{}, nil, unauthorizedCommand()
	default:
	}
	if err := h.Validate(ctx, credential.binding); err != nil {
		h.revokeCommandCredential(credential)
		return nil, GatewayRequest{}, nil, err
	}
	requestCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(credential.ctx, cancel)
	if credential.ctx.Err() != nil {
		stop()
		cancel()
		return nil, GatewayRequest{}, nil, unauthorizedCommand()
	}
	call.Binding, call.APIToken = credential.binding, ""
	return requestCtx, call, func() { stop(); cancel() }, nil
}

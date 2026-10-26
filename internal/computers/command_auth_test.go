package computers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/transport"
)

func commandAuthFixture(t *testing.T, handler transport.Handler) (*Hub, domain.ExecutionBinding, *connection) {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(store)
	computer := domain.Computer{Meta: domain.Meta{ID: domain.ID("computer")}, Name: "test"}
	agent := domain.Agent{Meta: domain.Meta{ID: domain.ID("agent")}, Name: "test", AllowedComputers: []string{computer.ID}}
	session := domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: agent.ID}
	for _, record := range []struct {
		kind string
		meta *domain.Meta
		body any
	}{{"computers", &computer.Meta, &computer}, {"agents", &agent.Meta, &agent}, {"sessions", &session.Meta, &session}} {
		if err = store.Save(t.Context(), record.kind, record.meta, record.body); err != nil {
			t.Fatal(err)
		}
	}
	left, right := net.Pipe()
	peer := transport.New(left, nil)
	remote := transport.New(right, handler)
	binding := domain.ExecutionBinding{GrantID: domain.ID("grant"), AgentID: agent.ID, SessionID: session.ID, ComputerID: computer.ID, BootID: domain.ID("boot"), APIURL: "http://127.0.0.1:1"}
	active := &connection{peer: peer, hello: Hello{BootID: binding.BootID}}
	hub.live[computer.ID], hub.grants[binding.GrantID] = active, binding
	t.Cleanup(func() { _ = hub.Close(); _ = remote.Close(); _ = store.Close() })
	return hub, binding, active
}

func TestCommandCredentialsDeriveIdentityAndStayPrivate(t *testing.T) {
	hub, binding, active := commandAuthFixture(t, nil)
	token, lease, err := hub.issueCommandCredential(t.Context(), binding, "execution", active.peer)
	if err != nil {
		t.Fatal(err)
	}
	if !validAPIToken(token) || lease.hash == token || lease.execution != "execution" {
		t.Fatal("credential is not an opaque execution-bound secret")
	}
	ctx, call, release, err := hub.commandRequest(t.Context(), active, GatewayRequest{Binding: binding, APIToken: token, Method: "GET", Path: "/v1/capabilities"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if ctx.Err() != nil || call.Binding != binding || call.APIToken != "" {
		t.Fatal("business handler did not receive only the authoritative binding")
	}
	for name, change := range map[string]func(*GatewayRequest){
		"missing":  func(call *GatewayRequest) { call.APIToken = "" },
		"unknown":  func(call *GatewayRequest) { call.APIToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" },
		"Bot":      func(call *GatewayRequest) { call.Binding.AgentID = "another_bot" },
		"Session":  func(call *GatewayRequest) { call.Binding.SessionID = "another_session" },
		"Computer": func(call *GatewayRequest) { call.Binding.ComputerID = "another_computer" },
		"grant":    func(call *GatewayRequest) { call.Binding.GrantID = "another_grant" },
	} {
		t.Run(name, func(t *testing.T) {
			forged := GatewayRequest{Binding: binding, APIToken: token}
			change(&forged)
			if _, _, cleanup, err := hub.commandRequest(t.Context(), active, forged); err == nil {
				cleanup()
				t.Fatal("accepted a credential with forged authority")
			}
		})
	}
	foreign := &connection{peer: active.peer, hello: active.hello}
	if _, _, cleanup, err := hub.commandRequest(t.Context(), foreign, GatewayRequest{Binding: binding, APIToken: token}); err == nil {
		cleanup()
		t.Fatal("accepted a credential from another connection with the same boot identity")
	}
	hub.revokeCommandCredential(lease)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel an in-flight API request")
	}
	if _, _, cleanup, err := hub.commandRequest(t.Context(), active, GatewayRequest{Binding: binding, APIToken: token}); err == nil {
		cleanup()
		t.Fatal("revoked credential remained usable")
	}
}

func TestCommandPermissionRemovalCancelsRequestsAndCannotRestoreOldToken(t *testing.T) {
	hub, binding, active := commandAuthFixture(t, nil)
	token, _, err := hub.issueCommandCredential(t.Context(), binding, "execution", active.peer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, _, release, err := hub.commandRequest(t.Context(), active, GatewayRequest{Binding: binding, APIToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var agent domain.Agent
	if err = hub.Store.Get(t.Context(), "agents", binding.AgentID, &agent); err != nil {
		t.Fatal(err)
	}
	agent.AllowedComputers = nil
	if err = hub.Store.Save(t.Context(), "agents", &agent.Meta, &agent); err != nil {
		t.Fatal(err)
	}
	hub.ReconcileAgentCommands(t.Context(), agent.ID)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("permission removal left an API request alive")
	}
	agent.AllowedComputers = []string{binding.ComputerID}
	if err = hub.Store.Save(t.Context(), "agents", &agent.Meta, &agent); err != nil {
		t.Fatal(err)
	}
	if _, _, cleanup, err := hub.commandRequest(t.Context(), active, GatewayRequest{Binding: binding, APIToken: token}); err == nil {
		cleanup()
		t.Fatal("restoring permission revived an old token")
	}
}

func TestCommandExitRevokesBeforeOutputIsDrained(t *testing.T) {
	hub, binding, active := commandAuthFixture(t, func(_ context.Context, request transport.Request, out *transport.Responder) error {
		if request.Method == "process.wait" {
			return out.JSON(processExit{Code: 0})
		}
		return out.JSON(struct{}{})
	})
	token, credential, err := hub.issueCommandCredential(t.Context(), binding, "execution", active.peer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	process := &remoteProcess{hub: hub, peer: active.peer, binding: binding, id: "process", ctx: ctx, cancel: cancel, credential: credential, outputDone: make(chan struct{})}
	if code, err := process.Wait(); err != nil || code != 0 {
		t.Fatalf("wait: exit=%d error=%v", code, err)
	}
	if _, _, cleanup, err := hub.commandRequest(t.Context(), active, GatewayRequest{Binding: binding, APIToken: token}); err == nil {
		cleanup()
		t.Fatal("completed command kept its credential while output was not drained")
	}
	close(process.outputDone)
}

func TestCommandCredentialsFailClosedAcrossConnectionReplacement(t *testing.T) {
	hub, binding, active := commandAuthFixture(t, nil)
	token, credential, err := hub.issueCommandCredential(t.Context(), binding, "execution", active.peer)
	if err != nil {
		t.Fatal(err)
	}
	replacement := &connection{peer: active.peer, hello: active.hello}
	hub.mu.Lock()
	hub.live[binding.ComputerID] = replacement
	hub.mu.Unlock()
	if _, _, cleanup, err := hub.commandRequest(t.Context(), replacement, GatewayRequest{Binding: binding, APIToken: token}); err == nil {
		cleanup()
		t.Fatal("reconnection inherited a previous connection's credential")
	}
	newToken, newCredential, err := hub.issueCommandCredential(t.Context(), binding, "new_execution", replacement.peer)
	if err != nil {
		t.Fatal(err)
	}
	hub.revokeConnectionCredentials(active)
	if credential.ctx.Err() == nil {
		t.Fatal("connection cleanup did not revoke its credential")
	}
	if newCredential.ctx.Err() != nil {
		t.Fatal("old connection cleanup revoked the new connection's credential")
	}
	_, _, release, err := hub.commandRequest(t.Context(), replacement, GatewayRequest{Binding: binding, APIToken: newToken})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, _, err := hub.issueCommandCredential(t.Context(), binding, "", active.peer); err == nil {
		t.Fatal("issued an unbound credential")
	}
	encoded, _ := json.Marshal(binding)
	if bytes.Contains(encoded, []byte(token)) || bytes.Contains(encoded, []byte("api_token")) {
		t.Fatal("credential appeared in the execution binding")
	}
	if !errors.Is(credential.ctx.Err(), context.Canceled) {
		t.Fatal("credential lifetime did not end")
	}
}

func TestCommandStartupFailureReleasesCredential(t *testing.T) {
	hub, binding, _ := commandAuthFixture(t, func(_ context.Context, request transport.Request, _ *transport.Responder) error {
		if request.Method != "process.start" {
			t.Error("unexpected process operation")
		}
		return domain.Invalid("test process could not start")
	})
	backend := &Backend{Hub: hub}
	_, err := backend.StartProcess(t.Context(), core.Invocation{SessionID: binding.SessionID, ExecutionID: "execution", Binding: BindingJSON(binding)}, standardtools.ProcessRequest{Command: "unused"})
	if err == nil {
		t.Fatal("startup failure was lost")
	}
	hub.mu.RLock()
	remaining := len(hub.commandCredentials)
	hub.mu.RUnlock()
	if remaining != 0 {
		t.Fatal("failed startup left an API credential behind")
	}
}

func TestCommandTerminationRevokesBeforeRemoteAcknowledgement(t *testing.T) {
	terminating, acknowledge := make(chan struct{}), make(chan struct{})
	hub, binding, active := commandAuthFixture(t, func(ctx context.Context, request transport.Request, out *transport.Responder) error {
		if request.Method == "process.terminate" {
			close(terminating)
			select {
			case <-acknowledge:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return out.JSON(struct{}{})
	})
	_, credential, err := hub.issueCommandCredential(t.Context(), binding, "execution", active.peer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	process := &remoteProcess{hub: hub, peer: active.peer, binding: binding, id: "process", ctx: ctx, cancel: cancel, credential: credential, output: io.NopCloser(strings.NewReader("")), outputDone: make(chan struct{})}
	finished := make(chan error, 1)
	go func() { finished <- process.Terminate(t.Context()) }()
	select {
	case <-terminating:
	case <-time.After(time.Second):
		t.Fatal("termination was not dispatched")
	}
	if credential.ctx.Err() == nil {
		t.Fatal("termination waited for the remote acknowledgement before revoking API access")
	}
	close(acknowledge)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestCommandOutputAttachmentCancellationCleansStartup(t *testing.T) {
	attaching, terminated := make(chan struct{}), make(chan struct{})
	hub, binding, _ := commandAuthFixture(t, func(ctx context.Context, request transport.Request, out *transport.Responder) error {
		switch request.Method {
		case "process.start":
			return out.JSON(processStart{ID: "process"})
		case "process.output":
			close(attaching)
			<-ctx.Done()
			return ctx.Err()
		case "process.terminate":
			close(terminated)
			return out.JSON(struct{}{})
		default:
			return domain.Invalid("unexpected operation")
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := (&Backend{Hub: hub}).StartProcess(ctx, core.Invocation{SessionID: binding.SessionID, ExecutionID: "execution", Binding: BindingJSON(binding)}, standardtools.ProcessRequest{Command: "unused"})
		finished <- err
	}()
	select {
	case <-attaching:
	case <-time.After(time.Second):
		t.Fatal("output attachment did not begin")
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled attachment returned a live process")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled output attachment kept startup alive")
	}
	select {
	case <-terminated:
	default:
		t.Fatal("the known remote process was not terminated")
	}
	hub.mu.RLock()
	remaining := len(hub.commandCredentials)
	hub.mu.RUnlock()
	if remaining != 0 {
		t.Fatal("cancelled attachment leaked a credential")
	}
}

func TestCommandAttachedProcessSurvivesCallerCancellation(t *testing.T) {
	issued := make(chan string, 1)
	hub, binding, active := commandAuthFixture(t, func(ctx context.Context, request transport.Request, out *transport.Responder) error {
		switch request.Method {
		case "process.start":
			var call Call
			if err := json.Unmarshal(request.Params, &call); err != nil {
				return err
			}
			issued <- call.APIToken
			return out.JSON(processStart{ID: "process"})
		case "process.output":
			if err := out.Headers(struct{}{}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		default:
			return out.JSON(struct{}{})
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	process, err := (&Backend{Hub: hub}).StartProcess(ctx, core.Invocation{SessionID: binding.SessionID, ExecutionID: "execution", Binding: BindingJSON(binding)}, standardtools.ProcessRequest{Command: "unused"})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { _ = process.Terminate(t.Context()) }()
	cancel()
	_, _, release, err := hub.commandRequest(t.Context(), active, GatewayRequest{Binding: binding, APIToken: <-issued})
	if err != nil {
		t.Fatalf("ending the tool call revoked its still-running command: %v", err)
	}
	release()
}

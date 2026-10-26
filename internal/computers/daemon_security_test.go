package computers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/transport"
)

func securityToken(value byte) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string(value), 32)))
}

func securityDaemon(t *testing.T, handler transport.Handler) (*Daemon, *daemonConnection) {
	t.Helper()
	d, err := NewDaemon(t.TempDir(), Credentials{ServerURL: "http://127.0.0.1:4545", ComputerID: "computer_security"})
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	generation := &daemonConnection{executions: map[string]string{}}
	d.mu.Lock()
	generation.peer = transport.New(left, func(ctx context.Context, request transport.Request, out *transport.Responder) error {
		return d.handle(ctx, generation, request, out)
	})
	d.connection, d.peer = generation, generation.peer
	d.mu.Unlock()
	server := transport.New(right, handler)
	t.Cleanup(func() {
		_ = server.Close()
		_ = d.Close(context.Background())
	})
	return d, generation
}

func securityBinding(t *testing.T, d *Daemon, grant string) domain.ExecutionBinding {
	t.Helper()
	b, err := d.environment(domain.ExecutionBinding{ComputerID: d.Credentials.ComputerID, BootID: d.Hello.BootID, GrantID: grant, AgentID: "agent_security", SessionID: "session_security"})
	if err == nil {
		b, err = d.ensureGateway(b)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type securityProcess struct {
	done             chan struct{}
	once             sync.Once
	terminated       atomic.Int32
	terminateErr     error
	terminateBlock   <-chan struct{}
	terminateEntered chan struct{}
}

func newSecurityProcess() *securityProcess { return &securityProcess{done: make(chan struct{})} }
func (p *securityProcess) Output() io.ReadCloser {
	return io.NopCloser(strings.NewReader("unread buffered output"))
}
func (p *securityProcess) Wait() (int, error)                  { <-p.done; return 0, nil }
func (p *securityProcess) Write(context.Context, []byte) error { return nil }
func (p *securityProcess) Interrupt(context.Context) error     { return nil }
func (p *securityProcess) Terminate(ctx context.Context) error {
	p.terminated.Add(1)
	if p.terminateEntered != nil {
		select {
		case p.terminateEntered <- struct{}{}:
		default:
		}
	}
	if p.terminateBlock != nil {
		select {
		case <-p.terminateBlock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if p.terminateErr != nil {
		return p.terminateErr
	}
	p.finish()
	return nil
}
func (p *securityProcess) finish() { p.once.Do(func() { close(p.done) }) }

func securityStart(t *testing.T, d *Daemon, generation *daemonConnection, binding domain.ExecutionBinding, token string) (string, *securityProcess) {
	t.Helper()
	process := newSecurityProcess()
	id, err := d.startProcess(context.Background(), generation, Call{Binding: binding, Execution: domain.ID("execution"), APIToken: token}, func(context.Context) (standardtools.Process, error) {
		return process, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return id, process
}

type forbiddenBody struct{ reads int }

func (b *forbiddenBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("body must not be read")
}
func (*forbiddenBody) Close() error { return nil }

func TestDaemonGatewayRejectsCredentialsBeforeReadingBody(t *testing.T) {
	d, generation := securityDaemon(t, nil)
	binding := securityBinding(t, d, "grant_security")
	_, process := securityStart(t, d, generation, binding, securityToken('a'))
	defer process.finish()
	for _, headers := range [][]string{
		nil, {"Basic ignored"}, {"Bearer malformed"}, {"Bearer " + securityToken('b')},
		{"Bearer " + securityToken('a') + " "}, {"Bearer " + securityToken('a'), "Bearer " + securityToken('a')},
	} {
		body := &forbiddenBody{}
		request := httptest.NewRequest(http.MethodPost, binding.APIURL+"/v1/test", body)
		for _, header := range headers {
			request.Header.Add("Authorization", header)
		}
		response := httptest.NewRecorder()
		d.gateways[binding.GrantID].server.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" || body.reads != 0 {
			t.Fatalf("credential rejection: status=%d challenge=%q body_reads=%d", response.Code, response.Header().Get("WWW-Authenticate"), body.reads)
		}
	}
}

func TestDaemonGatewayCredentialIsBoundToProcessAndConnection(t *testing.T) {
	var calls atomic.Int32
	token := securityToken('a')
	d, generation := securityDaemon(t, func(_ context.Context, request transport.Request, out *transport.Responder) error {
		var call GatewayRequest
		if err := json.Unmarshal(request.Params, &call); err != nil {
			return err
		}
		if call.APIToken != token || call.Intent != "stable-intent" || string(call.Body) != `{}` {
			t.Error("gateway lost explicit credential, body or intent")
		}
		calls.Add(1)
		return &transport.Error{Code: "unauthorized", Message: "Server revoked this credential", Outcome: "not_executed"}
	})
	a := securityBinding(t, d, "grant_a")
	b := securityBinding(t, d, "grant_b")
	id, process := securityStart(t, d, generation, a, token)
	request := httptest.NewRequest(http.MethodPost, a.APIURL+"/v1/test", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", "stable-intent")
	response := httptest.NewRecorder()
	d.gateways[a.GrantID].server.Handler.ServeHTTP(response, request)
	if calls.Load() != 1 || response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("Server authorization was not authoritative: calls=%d status=%d", calls.Load(), response.Code)
	}
	for _, binding := range []domain.ExecutionBinding{b, a} {
		if binding == a {
			process.finish()
			d.mu.Lock()
			done := d.processes[id].done
			d.mu.Unlock()
			<-done // Exit revokes before output is consumed or process.release.
		}
		body := &forbiddenBody{}
		request := httptest.NewRequest(http.MethodPost, binding.APIURL+"/v1/test", body)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		d.gateways[binding.GrantID].server.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || body.reads != 0 || calls.Load() != 1 {
			t.Fatalf("foreign or exited process credential was used: status=%d reads=%d calls=%d", response.Code, body.reads, calls.Load())
		}
	}
}

func TestDaemonDisconnectWaitsForReservedStartupAndReapsOnlyItsGeneration(t *testing.T) {
	d, generation := securityDaemon(t, nil)
	binding := securityBinding(t, d, "grant_security")
	entered, allow := make(chan struct{}), make(chan struct{})
	process := newSecurityProcess()
	started := make(chan error, 1)
	go func() {
		_, err := d.startProcess(context.Background(), generation, Call{Binding: binding, Execution: "execution_reserved", APIToken: securityToken('a')}, func(context.Context) (standardtools.Process, error) {
			close(entered)
			<-allow
			return process, nil
		})
		started <- err
	}()
	<-entered
	cleanup := make(chan error, 1)
	go func() { cleanup <- d.cleanupConnection(generation) }()
	deadline := time.After(3 * time.Second)
	for {
		d.mu.Lock()
		closing := generation.closing
		tokens := len(d.apiTokens)
		d.mu.Unlock()
		if closing {
			if tokens != 0 {
				t.Fatal("cleanup retained command credentials")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("cleanup did not seal generation")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-cleanup:
		t.Fatalf("cleanup skipped reserved startup: %v", err)
	default:
	}
	close(allow)
	if err := <-started; err == nil {
		t.Fatal("disconnected startup was reported as active")
	}
	if err := <-cleanup; err != nil {
		t.Fatal(err)
	}
	if process.terminated.Load() == 0 {
		t.Fatal("reserved process survived disconnection")
	}
	d.mu.Lock()
	if len(d.processes) != 0 || generation.executions != nil {
		t.Fatal("disconnected execution records were retained")
	}
	// A delayed handler from the closed generation cannot start a process even
	// if another connection is already active on the same daemon boot.
	newGeneration := &daemonConnection{peer: generation.peer, executions: map[string]string{}}
	d.connection, d.peer = newGeneration, newGeneration.peer
	d.mu.Unlock()
	called := false
	_, err := d.startProcess(context.Background(), generation, Call{Binding: binding, Execution: "execution_late", APIToken: securityToken('b')}, func(context.Context) (standardtools.Process, error) {
		called = true
		return newSecurityProcess(), nil
	})
	if err == nil || called {
		t.Fatal("old generation accepted new work")
	}
	_, newProcess := securityStart(t, d, newGeneration, binding, securityToken('c'))
	if err := d.cleanupConnection(generation); err != nil {
		t.Fatal(err)
	}
	if newProcess.terminated.Load() != 0 {
		t.Fatal("old cleanup terminated a new generation's process")
	}
}

func TestDaemonStartupFailureAndCleanupFailureAreFailClosed(t *testing.T) {
	d, generation := securityDaemon(t, nil)
	binding := securityBinding(t, d, "grant_security")
	call := Call{Binding: binding, Execution: "execution_failure", APIToken: securityToken('a')}
	_, err := d.startProcess(context.Background(), generation, call, func(context.Context) (standardtools.Process, error) {
		return nil, errors.New("OS startup failed")
	})
	if err == nil {
		t.Fatal("startup failure was hidden")
	}
	d.mu.Lock()
	if len(d.apiTokens) != 0 || len(d.processes) != 0 {
		t.Fatal("startup failure retained process credential")
	}
	d.mu.Unlock()
	_, err = d.startProcess(context.Background(), generation, call, func(context.Context) (standardtools.Process, error) {
		t.Fatal("failed execution was automatically dispatched twice")
		return nil, nil
	})
	if err == nil {
		t.Fatal("execution deduplication was lost after startup failure")
	}
	_, process := securityStart(t, d, generation, binding, securityToken('b'))
	process.terminateErr = errors.New("cannot terminate process tree")
	if err := d.cleanupConnection(generation); !errors.Is(err, errDaemonCleanup) {
		t.Fatalf("cleanup failure was hidden: %v", err)
	}
	d.mu.Lock()
	closed := d.closed
	tokens := len(d.apiTokens)
	d.mu.Unlock()
	if !closed || tokens != 0 {
		t.Fatal("failed cleanup did not stop the daemon and retire credentials")
	}
	process.finish()
}

func TestDaemonCloseWaitsForStartupReservation(t *testing.T) {
	d, generation := securityDaemon(t, nil)
	binding := securityBinding(t, d, "grant_security")
	entered, allow := make(chan struct{}), make(chan struct{})
	process := newSecurityProcess()
	started := make(chan error, 1)
	go func() {
		_, err := d.startProcess(context.Background(), generation, Call{Binding: binding, Execution: "execution_close", APIToken: securityToken('a')}, func(context.Context) (standardtools.Process, error) {
			close(entered)
			<-allow
			return process, nil
		})
		started <- err
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- d.Close(context.Background()) }()
	deadline := time.After(3 * time.Second)
	for {
		d.mu.Lock()
		closing := d.closed
		d.mu.Unlock()
		if closing {
			break
		}
		select {
		case <-deadline:
			t.Fatal("daemon did not begin closing")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-closed:
		t.Fatalf("Close skipped startup reservation: %v", err)
	default:
	}
	close(allow)
	if err := <-started; err == nil {
		t.Fatal("closing daemon reported startup success")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if process.terminated.Load() == 0 {
		t.Fatal("Close left a process running")
	}
}

func TestDaemonCommandCredentialUsesIsolatedChildEnvironment(t *testing.T) {
	d, _ := securityDaemon(t, nil)
	binding := securityBinding(t, d, "grant_security")
	t.Setenv(CommandAPITokenEnv, "parent-process-value")
	command := `printf '%s' "$ASTRAL_API_TOKEN"`
	if runtime.GOOS == "windows" {
		command = `[Console]::Write($env:ASTRAL_API_TOKEN)`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, token := range []string{securityToken('a'), securityToken('b')} {
		backend, err := d.backend(binding, token)
		if err != nil {
			t.Fatal(err)
		}
		process, err := backend.StartProcess(ctx, core.Invocation{}, standardtools.ProcessRequest{Command: command})
		if err != nil {
			t.Fatal(err)
		}
		output, readErr := io.ReadAll(process.Output())
		code, waitErr := process.Wait()
		if readErr != nil || waitErr != nil || code != 0 || string(output) != token {
			t.Fatalf("child environment was not isolated: code=%d read=%v wait=%v", code, readErr, waitErr)
		}
	}
	if os.Getenv(CommandAPITokenEnv) != "parent-process-value" {
		t.Fatal("command credential changed the daemon's global environment")
	}
}

func TestDaemonDisconnectCleansProcessesIndependently(t *testing.T) {
	d, generation := securityDaemon(t, nil)
	binding := securityBinding(t, d, "grant_security")
	_, blocked := securityStart(t, d, generation, binding, securityToken('a'))
	_, ordinary := securityStart(t, d, generation, binding, securityToken('b'))
	release := make(chan struct{})
	blocked.terminateBlock, blocked.terminateEntered = release, make(chan struct{}, 1)
	defer func() {
		if release != nil {
			close(release)
		}
	}()
	cleanup := make(chan error, 1)
	go func() { cleanup <- d.cleanupConnection(generation) }()
	select {
	case <-blocked.terminateEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked process cleanup was not attempted")
	}
	select {
	case <-ordinary.done:
	case <-time.After(time.Second):
		t.Fatal("one blocked termination prevented another process from being killed")
	}
	// Release the blocked fake without waiting for the production cleanup deadline.
	close(release)
	release = nil
	if err := <-cleanup; err != nil {
		t.Fatal(err)
	}
}

func TestDaemonConnectReapsBeforeReturningAndReconnectStartsFresh(t *testing.T) {
	hub, server := secureHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := NewDaemon(t.TempDir(), Credentials{ServerURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close(context.Background()) })
	enrollment, err := hub.Enroll(ctx, server.URL, false, false)
	if err != nil {
		t.Fatal(err)
	}
	d.Credentials, err = hub.Register(ctx, enrollment.Enrollment.ID, EnrollmentRequest{Token: enrollment.Token, Hello: d.Hello})
	if err != nil {
		t.Fatal(err)
	}
	d.Credentials.ServerURL = server.URL
	var previous *daemonConnection
	var binding domain.ExecutionBinding
	boot := d.Hello.BootID
	for i, token := range []string{securityToken('a'), securityToken('b')} {
		connected := make(chan error, 1)
		go func() { connected <- d.Connect(ctx) }()
		var generation *daemonConnection
		for generation == nil {
			d.mu.Lock()
			if current := d.connection; current != previous && d.connectedLocked(current) {
				generation = current
			}
			d.mu.Unlock()
			if generation == nil {
				select {
				case <-ctx.Done():
					t.Fatal("daemon did not connect")
				case <-time.After(time.Millisecond):
				}
			}
		}
		if i == 0 {
			binding = securityBinding(t, d, "grant_reconnect")
		}
		process := newSecurityProcess()
		// The same execution identity is legal on a new connection; old frames
		// cannot reach this generation and its old processes were already reaped.
		_, err := d.startProcess(ctx, generation, Call{Binding: binding, Execution: "execution_reused", APIToken: token}, func(context.Context) (standardtools.Process, error) {
			return process, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = generation.peer.Close()
		select {
		case err := <-connected:
			if !errors.Is(err, transport.ErrDisconnected) {
				t.Fatalf("unexpected disconnect result: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("Connect did not finish cleanup")
		}
		d.mu.Lock()
		retained := len(d.processes) + len(d.apiTokens)
		d.mu.Unlock()
		if retained != 0 || process.terminated.Load() == 0 || d.Hello.BootID != boot {
			t.Fatal("disconnected generation was not fully retired before reconnect")
		}
		previous = generation
	}
}

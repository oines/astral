package computers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral-core/standardtools/local"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/releasefeed"
	"github.com/oines/astral/internal/serviceurl"
	"github.com/oines/astral/internal/transport"
)

// CommandAPITokenEnv names the variable holding one command's Server-issued
// Computer API credential. API addresses remain in the environment snapshot.
const CommandAPITokenEnv = "ASTRAL_API_TOKEN"

var errDaemonCleanup = errors.New("daemon could not clean up disconnected commands")

type Daemon struct {
	Credentials Credentials
	Root        string
	Hello       Hello
	// Update is a Host management operation, never an Agent execution capability.
	Update     func(context.Context, releasefeed.Binary) error
	updateMu   sync.Mutex
	updating   bool
	httpClient *http.Client
	connectMu  sync.Mutex
	mu         sync.Mutex
	peer       *transport.Peer
	connection *daemonConnection
	gateways   map[string]*localGateway
	processes  map[string]*daemonProcess
	apiTokens  map[string]*daemonProcess
	closed     bool
}
type daemonConnection struct {
	peer        *transport.Peer
	executions  map[string]string
	closing     bool
	cleanupOnce sync.Once
	cleanupErr  error
}
type localGateway struct {
	binding domain.ExecutionBinding
	server  *http.Server
}
type daemonProcess struct {
	binding    domain.ExecutionBinding
	connection *daemonConnection
	apiToken   string
	process    standardtools.Process
	started    chan struct{}
	done       chan struct{}
	exit       processExit
	claimed    bool
}

func NewDaemon(root string, credentials Credentials) (*Daemon, error) {
	if err := serviceurl.Validate(credentials.ServerURL); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	shell := local.DefaultShell()
	client := NewHTTPClient()
	var container *domain.ContainerInfo
	if raw := os.Getenv("ASTRAL_CONTAINER_INFO"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &container); err != nil {
			return nil, fmt.Errorf("invalid container information: %w", err)
		}
	}
	return &Daemon{Root: root, Credentials: credentials, Hello: Hello{BootID: domain.ID("boot"), Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, Shell: shell, Home: home, Root: root, Container: container, Capabilities: []string{EnvironmentDiscoveryCapability}},
		httpClient: client, gateways: map[string]*localGateway{}, processes: map[string]*daemonProcess{}, apiTokens: map[string]*daemonProcess{}}, nil
}

// Run reconnects the same boot identity after reaping disconnected commands.
// No process is transparently recreated.
func (d *Daemon) Run(ctx context.Context, report func(error)) error {
	backoff := time.Second
	for ctx.Err() == nil {
		err := d.Connect(ctx)
		if report != nil {
			report(err)
		}
		d.mu.Lock()
		closed := d.closed
		d.mu.Unlock()
		if errors.Is(err, errDaemonCleanup) || closed {
			return err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		backoff = min(20*time.Second, backoff*2)
	}
	return ctx.Err()
}
func (d *Daemon) Connect(ctx context.Context) error {
	d.connectMu.Lock()
	defer d.connectMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return errors.New("daemon is closed")
	}
	conn, _, err := authenticateTunnel(ctx, d.Credentials.ServerURL, d.Credentials.ServerPin, tunnelRequest{Operation: "connect", ComputerID: d.Credentials.ComputerID, Token: d.Credentials.Token, Hello: d.Hello}, d.httpClient)
	if err != nil {
		return err
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		_ = conn.Close()
		return errors.New("daemon is closed")
	}
	generation := &daemonConnection{executions: map[string]string{}}
	peer := transport.New(conn, func(ctx context.Context, request transport.Request, out *transport.Responder) error {
		return d.handle(ctx, generation, request, out)
	})
	generation.peer = peer
	d.connection = generation
	d.peer = peer
	d.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { _ = peer.Close() })
	<-peer.Done()
	stop()
	return errors.Join(transport.ErrDisconnected, d.cleanupConnection(generation))
}

func (d *Daemon) connectedLocked(generation *daemonConnection) bool {
	if d.closed || generation == nil || d.connection != generation || generation.closing {
		return false
	}
	select {
	case <-generation.peer.Done():
		return false
	default:
		return true
	}
}

// cleanupConnection seals this generation before waiting for reserved startups.
// A disconnected connection can never leave a command behind or accept new work.
func (d *Daemon) cleanupConnection(generation *daemonConnection) error {
	if generation == nil {
		return nil
	}
	generation.cleanupOnce.Do(func() {
		d.mu.Lock()
		generation.closing = true
		if d.connection == generation {
			d.peer = nil
		}
		processes := make(map[string]*daemonProcess)
		for id, p := range d.processes {
			if p.connection == generation {
				processes[id] = p
				d.revokeProcessTokenLocked(p)
			}
		}
		d.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		results := make(chan error, len(processes))
		for _, p := range processes {
			go func() {
				results <- d.cleanupProcess(cleanup, p)
			}()
		}
		var errs []error
		for range processes {
			if err := <-results; err != nil {
				errs = append(errs, err)
			}
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if len(errs) != 0 {
			d.closed = true
			generation.cleanupErr = errors.Join(append([]error{errDaemonCleanup}, errs...)...)
			return
		}
		for id, p := range processes {
			if d.processes[id] == p {
				delete(d.processes, id)
			}
		}
		generation.executions = nil
		if d.connection == generation {
			d.connection = nil
		}
	})
	return generation.cleanupErr
}

func (d *Daemon) cleanupProcess(ctx context.Context, p *daemonProcess) error {
	select {
	case <-p.started:
	case <-ctx.Done():
		return ctx.Err()
	}
	d.mu.Lock()
	process := p.process
	d.mu.Unlock()
	if process == nil {
		return nil
	}
	if err := process.Terminate(ctx); err != nil {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Daemon) revokeProcessTokenLocked(p *daemonProcess) {
	if p.apiToken != "" {
		delete(d.apiTokens, p.apiToken)
		p.apiToken = ""
	}
}

func (d *Daemon) binding(b domain.ExecutionBinding) error {
	if b.ComputerID != d.Credentials.ComputerID || b.BootID != d.Hello.BootID || b.GrantID == "" ||
		!validIdentity(b.AgentID) || !validIdentity(b.SessionID) {
		return domain.Forbidden("execution binding does not match this daemon")
	}
	return nil
}
func validIdentity(value string) bool {
	if len(value) == 0 || len(value) > 150 {
		return false
	}
	for _, r := range value {
		if r < 'a' || r > 'z' {
			if r < 'A' || r > 'Z' {
				if r < '0' || r > '9' {
					if r != '_' && r != '-' {
						return false
					}
				}
			}
		}
	}
	return true
}
func (d *Daemon) environment(b domain.ExecutionBinding) (domain.ExecutionBinding, error) {
	b.AgentHome = filepath.Join(d.Root, "agents", b.AgentID, "home")
	if err := os.MkdirAll(b.AgentHome, 0700); err != nil {
		return b, err
	}
	if b.CWD == "" {
		b.CWD = b.AgentHome
	}
	if !filepath.IsAbs(b.CWD) {
		return b, domain.Invalid("working directory must be absolute on the target Computer")
	}
	if info, err := os.Stat(b.CWD); err != nil || !info.IsDir() {
		return b, domain.Invalid("working directory does not exist on the target Computer")
	}
	b.CWD, b.Shell = filepath.Clean(b.CWD), d.Hello.Shell
	return b, nil
}

func (d *Daemon) backend(b domain.ExecutionBinding, apiToken string) (*local.Backend, error) {
	d.mu.Lock()
	gateway := d.gateways[b.GrantID]
	d.mu.Unlock()
	if gateway == nil || gateway.binding != b {
		return nil, domain.Forbidden("execution environment was not prepared")
	}
	env := []string{"ASTRAL_AGENT_HOME=" + b.AgentHome}
	if apiToken != "" {
		env = append(env, CommandAPITokenEnv+"="+apiToken)
	}
	return local.New(local.Options{CWD: b.CWD, Shell: b.Shell, Env: env})
}

func (d *Daemon) localAPICredential(r *http.Request, binding domain.ExecutionBinding) (string, *transport.Peer) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", nil
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || !validAPIToken(token) {
		return "", nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.apiTokens[token]
	if p == nil || p.binding != binding || !d.connectedLocked(p.connection) {
		return "", nil
	}
	select {
	case <-p.done:
		return "", nil
	default:
		return token, p.connection.peer
	}
}

func rejectLocalAPI(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "a live command API credential is required", http.StatusUnauthorized)
}

func (d *Daemon) ensureGateway(b domain.ExecutionBinding) (domain.ExecutionBinding, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return b, domain.Offline("daemon is closed")
	}
	if g := d.gateways[b.GrantID]; g != nil {
		b.APIURL = g.binding.APIURL
		if g.binding != b {
			return b, domain.Forbidden("gateway binding changed")
		}
		return b, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return b, err
	}
	b.APIURL = "http://" + listener.Addr().String()
	g := &localGateway{binding: b}
	g.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A localhost endpoint is for command-line clients, never cross-origin
		// browser script. The server uses this listener's fixed binding.
		if r.Header.Get("Origin") != "" || r.Host != listener.Addr().String() {
			http.Error(w, "invalid local gateway origin", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/") || (r.Method != http.MethodGet && r.Method != http.MethodPost) {
			http.Error(w, "unsupported gateway operation", http.StatusNotFound)
			return
		}
		// Reject anonymous, malformed and locally retired credentials before
		// reading request bodies. The Server remains the final authority.
		apiToken, peer := d.localAPICredential(r, b)
		if peer == nil {
			rejectLocalAPI(w)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 48<<10))
		if err != nil || len(raw) > 0 && !json.Valid(raw) {
			http.Error(w, "gateway expects a small JSON body", http.StatusBadRequest)
			return
		}
		call := GatewayRequest{Binding: b, APIToken: apiToken, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: raw, Intent: r.Header.Get("Idempotency-Key")}
		var response GatewayResponse
		meta, body, err := peer.Call(r.Context(), "gateway", call, nil)
		if err == nil {
			err = json.Unmarshal(meta, &response)
		}
		if body != nil {
			defer body.Close()
		}
		if err != nil {
			status, code := http.StatusBadGateway, "outcome_unknown"
			var known *transport.Error
			if errors.As(err, &known) && known.Outcome == "not_executed" {
				status, code = http.StatusForbidden, known.Code
				if known.Code == "unauthorized" {
					status = http.StatusUnauthorized
					w.Header().Set("WWW-Authenticate", "Bearer")
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": err.Error()})
			return
		}
		for key, values := range response.Headers {
			w.Header()[key] = values
		}
		if response.Status < 100 || response.Status > 599 {
			response.Status = http.StatusBadGateway
		}
		w.WriteHeader(response.Status)
		if _, err := io.Copy(w, body); err != nil {
			panic(http.ErrAbortHandler)
		}
	})}
	d.gateways[b.GrantID] = g
	go func() { _ = g.server.Serve(listener) }()
	return b, nil
}

func (d *Daemon) handle(ctx context.Context, generation *daemonConnection, request transport.Request, out *transport.Responder) (err error) {
	defer func() {
		var tool *core.ToolError
		if errors.As(err, &tool) {
			err = &transport.Error{Code: tool.Code, Message: tool.Error(), Outcome: string(tool.Outcome)}
		}
	}()
	d.mu.Lock()
	connected := d.connectedLocked(generation)
	d.mu.Unlock()
	if !connected {
		return domain.Offline("daemon connection is no longer active")
	}
	if request.Method == DaemonUpdateCapability {
		var binary releasefeed.Binary
		if json.Unmarshal(request.Params, &binary) != nil {
			return domain.Invalid("invalid Daemon update")
		}
		if err := binary.Validate(d.Hello.OS, d.Hello.Arch); err != nil {
			return domain.Invalid(err.Error())
		}
		if d.Update == nil {
			return domain.Conflict("this Daemon has no managed updater; use its update command")
		}
		d.updateMu.Lock()
		if d.updating {
			d.updateMu.Unlock()
			return domain.Conflict("a Daemon update is already in progress")
		}
		if comparison, e := releasefeed.Compare(d.Hello.Version, binary.Version); e == nil && comparison >= 0 {
			d.updateMu.Unlock()
			if comparison > 0 {
				return domain.Conflict("Daemon updates cannot downgrade; update the Server first")
			}
			return out.JSON(struct{}{})
		}
		d.updating = true
		d.updateMu.Unlock()
		if err := d.Update(ctx, binary); err != nil {
			d.updateMu.Lock()
			d.updating = false
			d.updateMu.Unlock()
			return err
		}
		return out.JSON(struct{}{})
	}
	var call Call
	if json.Unmarshal(request.Params, &call) != nil {
		return domain.Invalid("invalid daemon call")
	}
	if err = d.binding(call.Binding); err != nil {
		return err
	}
	if request.Method == "environment" {
		b, err := d.environment(call.Binding)
		if err != nil {
			return err
		}
		if b, err = d.ensureGateway(b); err != nil {
			return err
		}
		return out.JSON(b)
	}
	apiToken := ""
	if request.Method == "process.start" {
		if !validAPIToken(call.APIToken) {
			return domain.Invalid("process API credential is required")
		}
		apiToken = call.APIToken
	}
	backend, err := d.backend(call.Binding, apiToken)
	if err != nil {
		return err
	}
	if request.Method == EnvironmentDiscoveryCapability {
		result, err := discoverEnvironment(ctx, d.Hello.Home, call.Binding)
		if err != nil {
			return err
		}
		return out.JSON(result)
	}
	if strings.HasPrefix(request.Method, "process.") {
		return d.processCall(ctx, generation, request, call, backend, out)
	}
	fs, err := backend.FileSystem(ctx, core.Invocation{})
	if err != nil {
		return err
	}
	switch request.Method {
	case "file.resolve":
		path, err := fs.ResolvePath(ctx, call.Path, call.ForRead)
		if err != nil {
			return err
		}
		path.Key = call.Binding.ComputerID + ":" + path.Key
		return out.JSON(path)
	case "file.mkdir":
		if err = fs.MkdirAll(ctx, call.Path); err != nil {
			return err
		}
		return out.JSON(struct{}{})
	case "file.stat", "file.read", "file.snapshot":
		path, err := fs.ResolvePath(ctx, call.Path, true)
		if err != nil {
			return err
		}
		if request.Method == "file.snapshot" {
			source, err := os.Open(path.Path)
			if err != nil {
				return err
			}
			defer source.Close()
			return d.storeMedia(ctx, call.Binding.AgentID, filepath.Base(path.Path), source, out)
		}
		file, err := os.Open(path.Path)
		if err != nil {
			return &transport.Error{Code: "file_unavailable", Message: err.Error(), Outcome: "not_executed"}
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			return domain.Invalid("file must be a regular file")
		}
		info := FileInfo{Path: path.Path, Name: stat.Name(), Size: stat.Size(), ModTime: stat.ModTime(), MediaType: mime.TypeByExtension(filepath.Ext(path.Path))}
		if info.MediaType == "" {
			info.MediaType = "application/octet-stream"
		}
		if call.Version != "" {
			hash := sha256.New()
			if _, err = copyContext(ctx, hash, file); err != nil {
				return err
			}
			info.Version = hex.EncodeToString(hash.Sum(nil))
			if info.Version != call.Version {
				return domain.Conflict("source file version changed")
			}
		}
		if request.Method == "file.stat" {
			return out.JSON(info)
		}
		if call.Offset < 0 || call.Offset > info.Size || call.Length != nil && *call.Length < 0 {
			return domain.Invalid("invalid file range")
		}
		if _, err = file.Seek(call.Offset, io.SeekStart); err != nil {
			return err
		}
		if err = out.Headers(info); err != nil {
			return err
		}
		var input io.Reader = file
		if call.Length != nil {
			input = io.LimitReader(file, *call.Length)
		}
		_, err = copyContext(ctx, out, input)
		after, statErr := file.Stat()
		if statErr != nil || after.Size() != stat.Size() || !after.ModTime().Equal(stat.ModTime()) {
			return domain.Conflict("source file changed during transfer")
		}
		return err
	case "file.write":
		// Core's write/edit semantics preserve symlinks and hard-linked files.
		path, err := fs.ResolvePath(ctx, call.Path, false)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(path.Path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
		if err != nil {
			return toolError(err, core.OutcomeNotExecuted)
		}
		_, writeErr := copyContext(ctx, file, request.Body)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
			return toolError(fmt.Errorf("file may be partially written: %w", err), core.OutcomeUnknown)
		}
		return out.JSON(struct{}{})
	case "file.upload":
		return d.storeMedia(ctx, call.Binding.AgentID, call.Path, request.Body, out)
	default:
		return domain.Missing("daemon method", request.Method)
	}
}

func (d *Daemon) storeMedia(ctx context.Context, agentID, name string, input io.Reader, out *transport.Responder) error {
	name = filepath.Base(name)
	if name == "." || name == "/" || name == "" {
		name = "attachment"
	}
	dir := filepath.Join(d.Root, "agents", agentID, "home", ".astral", "attachments", domain.ID("asset"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.RemoveAll(dir)
		}
	}()
	hash := sha256.New()
	size, err := copyContext(ctx, io.MultiWriter(file, hash), input)
	if err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	info := FileInfo{Path: path, Name: name, Size: size, ModTime: time.Now().UTC(), Version: hex.EncodeToString(hash.Sum(nil)), MediaType: mime.TypeByExtension(filepath.Ext(name))}
	if info.MediaType == "" {
		info.MediaType = "application/octet-stream"
	}
	complete = true
	return out.JSON(info)
}

func copyContext(ctx context.Context, out io.Writer, in io.Reader) (int64, error) {
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := in.Read(buffer)
		if n > 0 {
			written, writeErr := out.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return total, err
		}
	}
}

func (d *Daemon) startProcess(ctx context.Context, generation *daemonConnection, call Call, start func(context.Context) (standardtools.Process, error)) (string, error) {
	if call.Execution == "" || !validAPIToken(call.APIToken) {
		return "", domain.Invalid("process execution identity and API credential are required")
	}
	d.mu.Lock()
	if !d.connectedLocked(generation) {
		d.mu.Unlock()
		return "", domain.Offline("daemon connection is no longer active")
	}
	if generation.executions[call.Execution] != "" {
		d.mu.Unlock()
		return "", &transport.Error{Code: "already_dispatched", Message: "this execution was already dispatched; it will not be started again", Outcome: "unknown"}
	}
	if len(d.processes) >= 256 || d.apiTokens[call.APIToken] != nil {
		d.mu.Unlock()
		return "", domain.Conflict("daemon process capacity or credential is unavailable")
	}
	id := domain.ID("process")
	generation.executions[call.Execution] = id
	// Publish the credential before spawning: the child may call the API before
	// StartProcess returns. A disconnect must also account for this reservation.
	p := &daemonProcess{binding: call.Binding, connection: generation, apiToken: call.APIToken, started: make(chan struct{}), done: make(chan struct{})}
	d.processes[id] = p
	d.apiTokens[call.APIToken] = p
	d.mu.Unlock()
	process, err := start(ctx)
	d.mu.Lock()
	if err != nil {
		d.revokeProcessTokenLocked(p)
		delete(d.processes, id)
		close(p.started)
		close(p.done)
		d.mu.Unlock()
		return "", err
	}
	p.process = process
	connected := d.connectedLocked(generation)
	close(p.started)
	d.mu.Unlock()
	go func() {
		code, err := process.Wait()
		d.mu.Lock()
		p.exit.Code = code
		if err != nil {
			p.exit.Error = err.Error()
		}
		d.revokeProcessTokenLocked(p)
		close(p.done)
		d.mu.Unlock()
	}()
	if !connected {
		// Also terminate here if a blocked OS startup outlasted the connection's
		// cleanup deadline. Cleanup and Terminate are safe to run concurrently.
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := process.Terminate(cleanup)
		cancel()
		return "", errors.Join(domain.Offline("daemon connection was lost during process startup"), err)
	}
	return id, nil
}

func (d *Daemon) processCall(ctx context.Context, generation *daemonConnection, request transport.Request, call Call, backend *local.Backend, out *transport.Responder) error {
	if request.Method == "process.start" {
		id, err := d.startProcess(ctx, generation, call, func(ctx context.Context) (standardtools.Process, error) {
			return backend.StartProcess(ctx, core.Invocation{}, call.Process)
		})
		if err != nil {
			return err
		}
		return out.JSON(processStart{ID: id})
	}
	d.mu.Lock()
	p := d.processes[call.ProcessID]
	if !d.connectedLocked(generation) || p == nil || p.process == nil || p.binding != call.Binding || p.connection != generation {
		d.mu.Unlock()
		return domain.Missing("process on original Computer", call.ProcessID)
	}
	if request.Method == "process.output" {
		if p.claimed {
			d.mu.Unlock()
			return domain.Conflict("process output was already consumed")
		}
		p.claimed = true
	}
	d.mu.Unlock()
	switch request.Method {
	case "process.output":
		input := p.process.Output()
		stop := context.AfterFunc(ctx, func() { _ = input.Close() })
		defer stop()
		defer input.Close()
		if err := out.Headers(struct{}{}); err != nil {
			return err
		}
		_, err := copyContext(ctx, out, input)
		return err
	case "process.wait":
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return out.JSON(p.exit)
		}
	case "process.write":
		raw, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 {
			return domain.Invalid("process input exceeds 1 MiB")
		}
		if err = p.process.Write(ctx, raw); err != nil {
			return err
		}
	case "process.interrupt":
		if err := p.process.Interrupt(ctx); err != nil {
			return err
		}
	case "process.terminate":
		d.mu.Lock()
		d.revokeProcessTokenLocked(p)
		d.mu.Unlock()
		if err := p.process.Terminate(ctx); err != nil {
			return err
		}
	case "process.release":
		select {
		case <-p.done:
			d.mu.Lock()
			delete(d.processes, call.ProcessID)
			d.mu.Unlock()
		default:
			return domain.Conflict("process has not exited")
		}
	default:
		return domain.Missing("process operation", request.Method)
	}
	return out.JSON(struct{}{})
}

func (d *Daemon) Close(ctx context.Context) error {
	d.mu.Lock()
	d.closed = true
	peer := d.peer
	generation := d.connection
	gateways := make([]*http.Server, 0, len(d.gateways))
	for _, g := range d.gateways {
		gateways = append(gateways, g.server)
	}
	d.mu.Unlock()
	var errs []error
	if peer != nil {
		errs = append(errs, peer.Close())
	}
	errs = append(errs, d.cleanupConnection(generation))
	for _, gateway := range gateways {
		errs = append(errs, gateway.Close())
	}
	return errors.Join(errs...)
}

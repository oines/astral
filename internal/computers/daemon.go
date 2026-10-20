package computers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

	"github.com/coder/websocket"
	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral-core/standardtools/local"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/transport"
)

type Daemon struct {
	Credentials Credentials
	Root        string
	Hello       Hello
	mu          sync.Mutex
	peer        *transport.Peer
	gateways    map[string]*localGateway
	processes   map[string]*daemonProcess
	executions  map[string]string
	closed      bool
}
type localGateway struct {
	binding domain.ExecutionBinding
	url     string
	server  *http.Server
}
type daemonProcess struct {
	binding domain.ExecutionBinding
	process standardtools.Process
	done    chan struct{}
	exit    processExit
	claimed bool
}

func NewDaemon(root string, credentials Credentials) (*Daemon, error) {
	if err := modelbridge.ValidateURL(credentials.ServerURL); err != nil {
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
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return &Daemon{Root: root, Credentials: credentials, Hello: Hello{BootID: domain.ID("boot"), Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, Shell: shell, Home: home},
		gateways: map[string]*localGateway{}, processes: map[string]*daemonProcess{}, executions: map[string]string{}}, nil
}

// Run reconnects the same boot identity. No process is transparently recreated.
func (d *Daemon) Run(ctx context.Context, report func(error)) error {
	backoff := time.Second
	for ctx.Err() == nil {
		err := d.Connect(ctx)
		if report != nil {
			report(err)
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
	u := strings.TrimRight(d.Credentials.ServerURL, "/") + "/internal/computers/" + d.Credentials.ComputerID + "/connect"
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	hello, _ := json.Marshal(d.Hello)
	headers := http.Header{"Authorization": {"Bearer " + d.Credentials.Token}, "X-Astral-Hello": {base64.RawURLEncoding.EncodeToString(hello)}}
	conn, response, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		if response != nil {
			return fmt.Errorf("daemon connection rejected (HTTP %d)", response.StatusCode)
		}
		return fmt.Errorf("daemon cannot reach server: %w", err)
	}
	peer := transport.New(conn, d.handle)
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		_ = peer.Close()
		return errors.New("daemon is closed")
	}
	old := d.peer
	d.peer = peer
	d.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	stop := context.AfterFunc(ctx, func() { _ = peer.Close() })
	<-peer.Done()
	stop()
	d.mu.Lock()
	if d.peer == peer {
		d.peer = nil
	}
	d.mu.Unlock()
	return transport.ErrDisconnected
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

func (d *Daemon) backend(b domain.ExecutionBinding) (*local.Backend, error) {
	d.mu.Lock()
	gateway := d.gateways[b.GrantID]
	d.mu.Unlock()
	if gateway == nil || gateway.binding != b {
		return nil, domain.Forbidden("execution environment was not prepared")
	}
	return local.New(local.Options{CWD: b.CWD, Shell: b.Shell, Env: []string{"ASTRAL_API_URL=" + gateway.url, "ASTRAL_AGENT_HOME=" + b.AgentHome}})
}

func (d *Daemon) ensureGateway(b domain.ExecutionBinding) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return "", domain.Offline("daemon is closed")
	}
	if g := d.gateways[b.GrantID]; g != nil {
		if g.binding != b {
			return "", domain.Forbidden("gateway binding changed")
		}
		return g.url, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	g := &localGateway{binding: b, url: "http://" + listener.Addr().String()}
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
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 48<<10))
		if err != nil || len(raw) > 0 && !json.Valid(raw) {
			http.Error(w, "gateway expects a small JSON body", http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		peer := d.peer
		d.mu.Unlock()
		if peer == nil {
			http.Error(w, "server is disconnected; operation was not dispatched", http.StatusServiceUnavailable)
			return
		}
		call := GatewayRequest{Binding: b, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: raw, Intent: r.Header.Get("Idempotency-Key")}
		var response GatewayResponse
		if err = peer.JSON(r.Context(), "gateway", call, &response); err != nil {
			status, code := http.StatusBadGateway, "outcome_unknown"
			var known *transport.Error
			if errors.As(err, &known) && known.Outcome == "not_executed" {
				status, code = http.StatusForbidden, known.Code
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if response.Status < 100 || response.Status > 599 {
			response.Status = http.StatusBadGateway
		}
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	})}
	d.gateways[b.GrantID] = g
	go func() { _ = g.server.Serve(listener) }()
	return g.url, nil
}

func (d *Daemon) handle(ctx context.Context, request transport.Request, out *transport.Responder) (err error) {
	defer func() {
		var tool *core.ToolError
		if errors.As(err, &tool) {
			err = &transport.Error{Code: tool.Code, Message: tool.Error(), Outcome: string(tool.Outcome)}
		}
	}()
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
		if _, err = d.ensureGateway(b); err != nil {
			return err
		}
		return out.JSON(b)
	}
	backend, err := d.backend(call.Binding)
	if err != nil {
		return err
	}
	if strings.HasPrefix(request.Method, "process.") {
		return d.processCall(ctx, request, call, backend, out)
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

func (d *Daemon) processCall(ctx context.Context, request transport.Request, call Call, backend *local.Backend, out *transport.Responder) error {
	if request.Method == "process.start" {
		if call.Execution == "" {
			return domain.Invalid("process execution identity is required")
		}
		d.mu.Lock()
		if id := d.executions[call.Execution]; id != "" {
			d.mu.Unlock()
			return &transport.Error{Code: "already_dispatched", Message: "this execution was already dispatched; it will not be started again", Outcome: "unknown"}
		}
		if d.closed || len(d.processes) >= 256 {
			d.mu.Unlock()
			return domain.Conflict("daemon process capacity is unavailable")
		}
		id := domain.ID("process")
		d.executions[call.Execution] = id
		// Reserve before releasing the lock; process startup may overlap.
		p := &daemonProcess{binding: call.Binding, done: make(chan struct{})}
		d.processes[id] = p
		d.mu.Unlock()
		process, err := backend.StartProcess(ctx, core.Invocation{}, call.Process)
		d.mu.Lock()
		if err != nil {
			delete(d.processes, id)
			d.mu.Unlock()
			return err
		}
		p.process = process
		closed := d.closed
		d.mu.Unlock()
		go func() {
			code, err := process.Wait()
			p.exit.Code = code
			if err != nil {
				p.exit.Error = err.Error()
			}
			close(p.done)
		}()
		if closed {
			_ = process.Terminate(context.Background())
			return domain.Offline("daemon is closing")
		}
		return out.JSON(processStart{ID: id})
	}
	d.mu.Lock()
	p := d.processes[call.ProcessID]
	if p == nil || p.process == nil || p.binding != call.Binding {
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
	gateways := make([]*http.Server, 0, len(d.gateways))
	processes := make([]standardtools.Process, 0, len(d.processes))
	for _, g := range d.gateways {
		gateways = append(gateways, g.server)
	}
	for _, p := range d.processes {
		if p.process != nil {
			processes = append(processes, p.process)
		}
	}
	d.mu.Unlock()
	var errs []error
	if peer != nil {
		errs = append(errs, peer.Close())
	}
	for _, p := range processes {
		errs = append(errs, p.Terminate(ctx))
	}
	for _, gateway := range gateways {
		errs = append(errs, gateway.Close())
	}
	return errors.Join(errs...)
}

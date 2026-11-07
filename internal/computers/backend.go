package computers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/transport"
)

type Backend struct{ Hub *Hub }

func decodeBinding(inv core.Invocation) (domain.ExecutionBinding, error) {
	var b domain.ExecutionBinding
	if json.Unmarshal(inv.Binding, &b) != nil || b.SessionID != inv.SessionID {
		return b, domain.Forbidden("tool invocation has no matching execution binding")
	}
	return b, nil
}
func remoteError(err error, mutation bool) error {
	if err == nil {
		return nil
	}
	outcome := core.OutcomeNotExecuted
	if mutation {
		outcome = core.OutcomeUnknown
	}
	var known *transport.Error
	if errors.As(err, &known) && known.Outcome != "" {
		outcome = core.Outcome(known.Outcome)
	}
	var host *domain.Error
	if errors.As(err, &host) {
		outcome = core.OutcomeNotExecuted
	}
	return toolError(err, outcome)
}
func (b *Backend) FileSystem(ctx context.Context, inv core.Invocation) (standardtools.FileSystem, error) {
	binding, err := decodeBinding(inv)
	if err != nil {
		return nil, remoteError(err, false)
	}
	if err = b.Hub.Validate(ctx, binding); err != nil {
		return nil, remoteError(err, false)
	}
	return &remoteFS{hub: b.Hub, binding: binding}, nil
}

type remoteFS struct {
	hub     *Hub
	binding domain.ExecutionBinding
}

func (f *remoteFS) ResolvePath(ctx context.Context, path string, forRead bool) (standardtools.ResolvedPath, error) {
	var result standardtools.ResolvedPath
	err := f.hub.JSON(ctx, "file.resolve", Call{Binding: f.binding, Path: path, ForRead: forRead}, &result)
	return result, remoteError(err, false)
}
func (f *remoteFS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	meta, stream, err := f.hub.Call(ctx, "file.read", Call{Binding: f.binding, Path: path}, nil)
	if err != nil {
		return nil, remoteError(err, false)
	}
	defer stream.Close()
	var info FileInfo
	if err = json.Unmarshal(meta, &info); err != nil {
		return nil, err
	}
	if info.Size < 0 {
		return nil, domain.Invalid("file response has an invalid size")
	}
	if info.Size > 32<<20 {
		return nil, domain.Invalid("file exceeds the 32 MiB tool read limit; use a shell command for selected ranges")
	}
	raw, err := io.ReadAll(io.LimitReader(stream, (32<<20)+1))
	if len(raw) > 32<<20 {
		return nil, domain.Invalid("file exceeds the 32 MiB tool read limit")
	}
	if err != nil {
		return nil, remoteError(err, false)
	}
	if int64(len(raw)) != info.Size {
		return nil, remoteError(errors.New("file transfer did not match the advertised size"), false)
	}
	return raw, nil
}
func (f *remoteFS) WriteFile(ctx context.Context, path string, data []byte) error {
	if len(data) > fileWriteSizeLimit {
		return remoteError(domain.Invalid("file exceeds the 32 MiB tool write limit; use a shell command for larger files"), false)
	}
	return f.mutate(ctx, "file.write", path, bytes.NewReader(data))
}

func (f *remoteFS) mutate(ctx context.Context, method, path string, data io.Reader) error {
	peer, err := f.hub.Peer(ctx, f.binding)
	if err != nil {
		return remoteError(err, false)
	}
	if err := ctx.Err(); err != nil {
		return remoteError(err, false)
	}
	// Once dispatched, wait for the file operation's terminal response before
	// releasing the SDK's file queue. Caller cancellation must not let a later
	// edit read a partial file while the daemon is still writing it. The pinned
	// peer still terminates on disconnect, whose mutation outcome stays unknown.
	_, stream, err := peer.Call(context.WithoutCancel(ctx), method, Call{Binding: f.binding, Path: path}, data)
	if err != nil {
		return remoteError(err, true)
	}
	defer stream.Close()
	_, err = io.Copy(io.Discard, stream)
	return remoteError(err, true)
}
func (f *remoteFS) MkdirAll(ctx context.Context, path string) error {
	return f.mutate(ctx, "file.mkdir", path, nil)
}

func (b *Backend) StartProcess(ctx context.Context, inv core.Invocation, request standardtools.ProcessRequest) (standardtools.Process, error) {
	binding, err := decodeBinding(inv)
	if err != nil {
		return nil, remoteError(err, false)
	}
	peer, err := b.Hub.Peer(ctx, binding)
	if err != nil {
		return nil, remoteError(err, false)
	}
	token, credential, err := b.Hub.issueCommandCredential(ctx, binding, inv.ExecutionID, peer)
	if err != nil {
		return nil, remoteError(err, false)
	}
	started := false
	lifetime, cancel := context.WithCancel(context.Background())
	p := &remoteProcess{hub: b.Hub, peer: peer, binding: binding, ctx: lifetime, cancel: cancel, credential: credential}
	defer func() {
		if !started {
			b.Hub.revokeCommandCredential(credential)
			cancel()
		}
	}()
	var start processStart
	var output io.ReadCloser
	b.Hub.mu.RLock()
	active := b.Hub.live[binding.ComputerID]
	streamStart := active != nil && active.peer == peer && slices.Contains(active.hello.Capabilities, ProcessStreamCapability)
	b.Hub.mu.RUnlock()
	if streamStart {
		// Only startup belongs to the invocation context. The same stream then
		// carries output for a yielded process, without another network round trip.
		stop := context.AfterFunc(ctx, cancel)
		var metadata json.RawMessage
		metadata, output, err = peer.Call(lifetime, ProcessStreamCapability, Call{Binding: binding, Execution: inv.ExecutionID, Process: request, APIToken: token}, nil)
		stop()
		if err == nil {
			err = json.Unmarshal(metadata, &start)
		}
		if err == nil && start.ID == "" {
			err = errors.New("daemon returned no process identity")
		}
		if err == nil {
			err = ctx.Err()
		}
		p.id = start.ID
	} else {
		// Older Daemons expose startup and output as separate calls.
		if err = peer.JSON(ctx, "process.start", Call{Binding: binding, Execution: inv.ExecutionID, Process: request, APIToken: token}, &start); err != nil {
			return nil, remoteError(err, true)
		}
		p.id = start.ID
		attachCtx, cancelAttach := context.WithTimeout(ctx, 10*time.Second)
		stopAttach := context.AfterFunc(attachCtx, cancel)
		_, output, err = peer.Call(lifetime, "process.output", p.call(), nil)
		stopAttach()
		if err == nil {
			err = attachCtx.Err()
		}
		cancelAttach()
	}
	if err != nil {
		b.Hub.revokeCommandCredential(credential)
		if output != nil {
			_ = output.Close()
		}
		if p.id != "" {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			_ = peer.JSON(cleanup, "process.terminate", p.call(), nil)
			stop()
		}
		return nil, remoteError(err, true)
	}
	p.outputDone = make(chan struct{})
	p.output = &processOutput{ReadCloser: output, done: p.outputDone}
	started = true
	return p, nil
}

type remoteProcess struct {
	hub        *Hub
	peer       *transport.Peer
	binding    domain.ExecutionBinding
	id         string
	output     io.ReadCloser
	outputDone chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	release    sync.Once
	credential *commandCredential
}

func (p *remoteProcess) call() Call { return Call{Binding: p.binding, ProcessID: p.id} }
func (p *remoteProcess) current(ctx context.Context) error {
	peer, err := p.hub.Peer(ctx, p.binding)
	if err != nil {
		return err
	}
	if peer != p.peer {
		return domain.Offline("the original process stream disconnected; its outcome requires inspection")
	}
	return nil
}
func (p *remoteProcess) Output() io.ReadCloser { return p.output }
func (p *remoteProcess) Wait() (int, error) {
	defer p.hub.revokeCommandCredential(p.credential)
	var exit processExit
	err := p.peer.JSON(p.ctx, "process.wait", p.call(), &exit)
	if err != nil {
		return 0, remoteError(err, true)
	}
	// Release only after the output stream has been drained. Terminate or the
	// host's tool-set cleanup handles early shutdown independently.
	go func() {
		select {
		case <-p.ctx.Done():
		case <-p.peer.Done():
		case <-p.outputDone:
		}
		p.releaseProcess()
	}()
	if exit.Error != "" {
		return exit.Code, errors.New(exit.Error)
	}
	return exit.Code, nil
}

type processOutput struct {
	io.ReadCloser
	done chan struct{}
	once sync.Once
}

func (o *processOutput) Read(data []byte) (int, error) {
	n, err := o.ReadCloser.Read(data)
	if err != nil {
		o.once.Do(func() { close(o.done) })
	}
	return n, err
}
func (o *processOutput) Close() error {
	err := o.ReadCloser.Close()
	o.once.Do(func() { close(o.done) })
	return err
}
func (p *remoteProcess) Write(ctx context.Context, data []byte) error {
	if err := p.current(ctx); err != nil {
		return remoteError(err, false)
	}
	_, body, err := p.peer.Call(ctx, "process.write", p.call(), bytes.NewReader(data))
	if err != nil {
		return remoteError(err, true)
	}
	defer body.Close()
	_, err = io.Copy(io.Discard, body)
	return remoteError(err, true)
}
func (p *remoteProcess) Interrupt(ctx context.Context) error {
	if err := p.current(ctx); err != nil {
		return remoteError(err, false)
	}
	return remoteError(p.peer.JSON(ctx, "process.interrupt", p.call(), nil), true)
}
func (p *remoteProcess) Terminate(ctx context.Context) error {
	p.hub.revokeCommandCredential(p.credential)
	err := p.peer.JSON(ctx, "process.terminate", p.call(), nil)
	_ = p.output.Close()
	p.cancel()
	p.releaseProcess()
	return remoteError(err, true)
}
func (p *remoteProcess) releaseProcess() {
	p.release.Do(func() {
		p.hub.revokeCommandCredential(p.credential)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.peer.JSON(ctx, "process.release", p.call(), nil)
		p.cancel()
	})
}

func BindingJSON(binding domain.ExecutionBinding) json.RawMessage {
	raw, err := json.Marshal(binding)
	if err != nil {
		panic(fmt.Sprintf("marshal execution binding: %v", err))
	}
	return raw
}

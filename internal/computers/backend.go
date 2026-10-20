package computers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if info.Size > 32<<20 {
		return nil, domain.Invalid("file exceeds the 32 MiB tool read limit; use a shell command for selected ranges")
	}
	raw, err := io.ReadAll(io.LimitReader(stream, (32<<20)+1))
	if len(raw) > 32<<20 {
		return nil, domain.Invalid("file exceeds the 32 MiB tool read limit")
	}
	return raw, remoteError(err, false)
}
func (f *remoteFS) WriteFile(ctx context.Context, path string, data []byte) error {
	_, stream, err := f.hub.Call(ctx, "file.write", Call{Binding: f.binding, Path: path}, bytes.NewReader(data))
	if err != nil {
		return remoteError(err, true)
	}
	defer stream.Close()
	_, err = io.Copy(io.Discard, stream)
	return remoteError(err, true)
}
func (f *remoteFS) MkdirAll(ctx context.Context, path string) error {
	return remoteError(f.hub.JSON(ctx, "file.mkdir", Call{Binding: f.binding, Path: path}, nil), true)
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
	var start processStart
	if err = peer.JSON(ctx, "process.start", Call{Binding: binding, Execution: inv.ExecutionID, Process: request}, &start); err != nil {
		return nil, remoteError(err, true)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	p := &remoteProcess{hub: b.Hub, peer: peer, binding: binding, id: start.ID, ctx: lifetime, cancel: cancel}
	_, output, err := peer.Call(lifetime, "process.output", p.call(), nil)
	if err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		_ = peer.JSON(cleanup, "process.terminate", p.call(), nil)
		stop()
		cancel()
		return nil, remoteError(err, true)
	}
	p.outputDone = make(chan struct{})
	p.output = &processOutput{ReadCloser: output, done: p.outputDone}
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
	err := p.peer.JSON(ctx, "process.terminate", p.call(), nil)
	_ = p.output.Close()
	p.cancel()
	p.releaseProcess()
	return remoteError(err, true)
}
func (p *remoteProcess) releaseProcess() {
	p.release.Do(func() {
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

package computers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/transport"
)

func TestCanceledRemoteEditKeepsFileQueueUntilWriteSettles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0600); err != nil {
		t.Fatal(err)
	}
	partial, release, secondResolved := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var resolves, reads, writes atomic.Int32
	hub, binding, _ := commandAuthFixture(t, func(_ context.Context, request transport.Request, out *transport.Responder) error {
		switch request.Method {
		case "file.resolve":
			if resolves.Add(1) == 2 {
				close(secondResolved)
			}
			return out.JSON(standardtools.ResolvedPath{Path: path, Parent: filepath.Dir(path), Key: path})
		case "file.read":
			reads.Add(1)
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := out.Headers(FileInfo{Size: int64(len(data))}); err != nil {
				return err
			}
			_, err = out.Write(data)
			return err
		case "file.write":
			data, err := io.ReadAll(request.Body)
			if err != nil {
				return err
			}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
			if err != nil {
				return err
			}
			defer file.Close()
			if writes.Add(1) == 1 {
				if _, err := file.Write(data[:6]); err != nil {
					return err
				}
				close(partial)
				<-release
				data = data[6:]
			}
			if _, err := file.Write(data); err != nil {
				return err
			}
			if err := file.Sync(); err != nil {
				return err
			}
			return out.JSON(struct{}{})
		default:
			return errors.New("unexpected file method: " + request.Method)
		}
	})
	set, err := standardtools.New(&Backend{Hub: hub})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close(context.Background()) })
	var edit core.Tool
	for _, tool := range set.Tools() {
		if tool.Definition().Name == "edit" {
			edit = tool
		}
	}
	if edit == nil {
		t.Fatal("missing SDK edit tool")
	}
	invoke := func(ctx context.Context, old, next string) error {
		args, err := json.Marshal(standardtools.EditRequest{Path: path, Edits: []standardtools.Edit{{OldText: old, NewText: next}}})
		if err != nil {
			return err
		}
		_, err = edit.Execute(ctx, core.Invocation{SessionID: binding.SessionID, Binding: BindingJSON(binding), Call: core.ToolCall{Name: "edit", Arguments: args}}, nil)
		return err
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- invoke(ctx, "alpha", "ALPHA") }()
	select {
	case <-partial:
	case <-time.After(3 * time.Second):
		t.Fatal("first real file write did not reach its partial-write gate")
	}
	cancel()
	go func() { second <- invoke(t.Context(), "beta", "BETA") }()
	select {
	case <-secondResolved:
	case <-time.After(3 * time.Second):
		t.Fatal("second edit did not reach the SDK file queue")
	}
	select {
	case err := <-first:
		t.Fatalf("canceled edit released the SDK queue before its remote write settled: %v", err)
	case err := <-second:
		t.Fatalf("second edit escaped the SDK queue while the file was partial: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if reads.Load() != 1 || writes.Load() != 1 {
		t.Fatalf("second edit read or wrote a partial file: reads=%d writes=%d", reads.Load(), writes.Load())
	}
	releaseOnce.Do(func() { close(release) })
	for name, done := range map[string]<-chan error{"first": first, "second": second} {
		select {
		case err := <-done:
			if name == "second" && err != nil {
				t.Fatalf("second edit failed after the first write settled: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s edit did not finish", name)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "ALPHA\nBETA\n" {
		t.Fatalf("later edit lost the settled first edit: %q, %v", data, err)
	}
}

func TestRemoteFileMutationCancellationBeforeStartDoesNotDispatch(t *testing.T) {
	var calls atomic.Int32
	hub, binding, _ := commandAuthFixture(t, func(context.Context, transport.Request, *transport.Responder) error {
		calls.Add(1)
		return nil
	})
	fs := &remoteFS{hub: hub, binding: binding}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, mutate := range map[string]func() error{
		"write": func() error { return fs.WriteFile(ctx, "file", []byte("data")) },
		"mkdir": func() error { return fs.MkdirAll(ctx, "directory") },
	} {
		var tool *core.ToolError
		if err := mutate(); !errors.As(err, &tool) || tool.Outcome != core.OutcomeNotExecuted {
			t.Fatalf("%s pre-canceled mutation outcome: %v", name, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("pre-canceled mutation reached the daemon")
	}
}

func TestDisconnectedRemoteWriteRetainsUnknownOutcome(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	hub, binding, active := commandAuthFixture(t, func(ctx context.Context, request transport.Request, _ *transport.Responder) error {
		if _, err := io.ReadAll(request.Body); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		<-release
		return ctx.Err()
	})
	fs := &remoteFS{hub: hub, binding: binding}
	done := make(chan error, 1)
	go func() { done <- fs.WriteFile(t.Context(), "file", []byte("data")) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("remote write did not start")
	}
	_ = active.peer.Close()
	select {
	case err := <-done:
		var tool *core.ToolError
		if !errors.Is(err, transport.ErrDisconnected) || !errors.As(err, &tool) || tool.Outcome != core.OutcomeUnknown {
			t.Fatalf("disconnected write lost its unknown outcome: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disconnected write did not return")
	}
}

type heldFileBody struct {
	reader  *strings.Reader
	entered chan struct{}
	release <-chan struct{}
	reads   int
}

func (b *heldFileBody) Read(data []byte) (int, error) {
	b.reads++
	if b.reads == 1 {
		return b.reader.Read(data[:min(len(data), 6)])
	}
	if b.reads == 2 {
		close(b.entered)
		<-b.release
	}
	return b.reader.Read(data)
}

func TestDaemonReconnectWaitsForDisconnectedFileMutation(t *testing.T) {
	hub, server := secureHub(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
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
	connect := func() <-chan error {
		done := make(chan error, 1)
		go func() { done <- d.Connect(ctx) }()
		return done
	}
	firstConnection := connect()
	var generation *daemonConnection
	for generation == nil {
		d.mu.Lock()
		if d.connectedLocked(d.connection) {
			generation = d.connection
		}
		d.mu.Unlock()
		if generation == nil {
			select {
			case err := <-firstConnection:
				t.Fatalf("initial connection failed: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	binding := securityBinding(t, d, "grant_file_settle")
	path := filepath.Join(binding.CWD, "settle.txt")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	body := &heldFileBody{reader: strings.NewReader("ALPHA\nbeta\n"), entered: entered, release: release}
	params, err := json.Marshal(Call{Binding: binding, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	writeCtx, stopWrite := context.WithCancel(ctx)
	defer stopWrite()
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- d.handle(writeCtx, generation, transport.Request{Method: "file.write", Params: params, Body: body}, nil)
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("daemon write did not reach its partial-file gate")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "ALPHA\n" {
		t.Fatalf("write gate did not hold a real partially-written file: %q, %v", data, err)
	}
	stopWrite()
	_ = generation.peer.Close()
	secondConnection := connect()
	select {
	case err := <-firstConnection:
		t.Fatalf("old Connect returned before its file mutation settled: %v", err)
	case err := <-secondConnection:
		t.Fatalf("new Connect passed the unsettled old mutation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	d.mu.Lock()
	oldStillCurrent := d.connection == generation
	d.mu.Unlock()
	if !oldStillCurrent {
		t.Fatal("a new connection became live while its old write was unsettled")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-writeDone:
		var wire *transport.Error
		if !errors.As(err, &wire) || wire.Outcome != string(core.OutcomeUnknown) {
			t.Fatalf("partial disconnected daemon write lost unknown outcome: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("old daemon write did not settle")
	}
	select {
	case err := <-firstConnection:
		if !errors.Is(err, transport.ErrDisconnected) {
			t.Fatalf("unexpected old connection result: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("old connection did not finish after its write settled")
	}
	for {
		d.mu.Lock()
		current := d.connection
		ready := current != generation && d.connectedLocked(current)
		d.mu.Unlock()
		if ready {
			break
		}
		select {
		case err := <-secondConnection:
			t.Fatalf("reconnection failed: %v", err)
		case <-ctx.Done():
			t.Fatal("reconnection did not proceed after settlement")
		case <-time.After(time.Millisecond):
		}
	}
	hub.mu.RLock()
	active := hub.live[d.Credentials.ComputerID]
	hub.mu.RUnlock()
	_, stream, err := active.peer.Call(ctx, "file.write", Call{Binding: binding, Path: path}, strings.NewReader("FINAL\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, stream)
	_ = stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "FINAL\n" {
		t.Fatalf("old generation overwrote the new connection's write: %q, %v", data, err)
	}
	_ = d.Close(ctx)
	select {
	case <-secondConnection:
	case <-ctx.Done():
		t.Fatal("reconnected daemon did not close")
	}
}

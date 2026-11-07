package computers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/domain"
	testshell "github.com/oines/astral/internal/testkit/shell"
	"github.com/oines/astral/internal/transport"
)

// A byte-rate limit creates real transport backpressure without corrupting
// frames. Packet loss belongs below TCP; dropping RPC frames would test a
// different, invalid protocol.
type rateLimitedComputerConn struct {
	net.Conn
	bytesPerSecond int
	closed         chan struct{}
	once           sync.Once
}

func (c *rateLimitedComputerConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (c *rateLimitedComputerConn) Write(data []byte) (int, error) {
	total := 0
	for len(data) > 0 {
		n := min(len(data), 4<<10)
		timer := time.NewTimer(time.Duration(n) * time.Second / time.Duration(c.bytesPerSecond))
		select {
		case <-timer.C:
		case <-c.closed:
			timer.Stop()
			return total, net.ErrClosed
		}
		written, err := c.Conn.Write(data[:n])
		total += written
		if err != nil {
			return total, err
		}
		if written != n {
			return total, io.ErrShortWrite
		}
		data = data[n:]
	}
	return total, nil
}

func slowLinkDaemon(t *testing.T, bytesPerSecond int) (*Daemon, *transport.Peer, domain.ExecutionBinding) {
	t.Helper()
	d, err := NewDaemon(t.TempDir(), Credentials{ServerURL: "http://127.0.0.1:4545", ComputerID: "synthetic_computer"})
	if err != nil {
		t.Fatal(err)
	}
	d.Hello.Home = t.TempDir()
	binding := securityBinding(t, d, "synthetic_grant")
	left, right := net.Pipe()
	var daemonConn net.Conn = left
	if bytesPerSecond > 0 {
		daemonConn = &rateLimitedComputerConn{Conn: left, bytesPerSecond: bytesPerSecond, closed: make(chan struct{})}
	}
	generation := &daemonConnection{executions: map[string]string{}}
	generation.peer = transport.New(daemonConn, func(ctx context.Context, request transport.Request, out *transport.Responder) error {
		return d.handle(ctx, generation, request, out)
	})
	d.connection, d.peer = generation, generation.peer
	server := transport.New(right, nil)
	t.Cleanup(func() {
		_ = server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.Close(ctx); err != nil {
			t.Errorf("close synthetic Daemon: %v", err)
		}
	})
	return d, server, binding
}

func TestDaemonLateOutputAttachmentKeepsShortCommand(t *testing.T) {
	for _, tty := range []bool{false, true} {
		name := "pipe"
		if tty {
			name = "tty"
		}
		t.Run(name, func(t *testing.T) {
			d, server, binding := slowLinkDaemon(t, 0)
			call := Call{Binding: binding, Execution: "synthetic_execution", APIToken: securityToken('s'),
				Process: standardtools.ProcessRequest{Command: testshell.Print("synthetic stdout marker"), TTY: tty}}
			var started processStart
			if err := server.JSON(t.Context(), "process.start", call, &started); err != nil {
				t.Fatal(err)
			}
			d.mu.Lock()
			process := d.processes[started.ID]
			d.mu.Unlock()
			select {
			case <-process.done:
			case <-time.After(10 * time.Second):
				t.Fatal("short command did not finish independently of output attachment")
			}
			// Exceeds the pinned local backend's post-exit 500ms pipe deadline.
			time.Sleep(750 * time.Millisecond)
			call.ProcessID = started.ID
			_, stream, err := server.Call(t.Context(), "process.output", call, nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(stream)
			_ = stream.Close()
			if err != nil || !tty && string(raw) != "synthetic stdout marker" || tty && strings.Count(string(raw), "synthetic stdout marker") != 1 {
				t.Fatalf("late output: %q, %v", raw, err)
			}
			var exit processExit
			if err := server.JSON(t.Context(), "process.wait", call, &exit); err != nil || exit.Code != 0 || exit.Error != "" {
				t.Fatalf("late output status: %+v, %v", exit, err)
			}
			if err := server.JSON(t.Context(), "process.release", call, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type capturedSyntheticProcess struct {
	*securityProcess
	output io.ReadCloser
}

func (p *capturedSyntheticProcess) Output() io.ReadCloser { return p.output }

func TestDaemonWaitReportsSpoolFailureAfterProcessExit(t *testing.T) {
	d, server, binding := slowLinkDaemon(t, 0)
	source := &capturedSyntheticProcess{securityProcess: newSecurityProcess(), output: io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("synthetic"), 20<<10)))}
	id, err := d.startProcess(t.Context(), d.connection, Call{Binding: binding, Execution: "synthetic_execution", APIToken: securityToken('s')},
		func(context.Context) (standardtools.Process, error) { return source, nil })
	if err != nil {
		t.Fatal(err)
	}
	source.finish()
	d.mu.Lock()
	process := d.processes[id]
	d.mu.Unlock()
	select {
	case <-process.done:
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic process did not exit")
	}
	process.output.mu.Lock()
	var spool string
	for _, segment := range process.output.segments {
		if segment.path != "" {
			spool = segment.path
			break
		}
	}
	process.output.mu.Unlock()
	if spool == "" {
		t.Fatal("synthetic output did not exercise disk spooling")
	}
	if err := os.Remove(spool); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, process.output); err == nil {
		t.Fatal("missing spool returned a normal EOF")
	}
	var exit processExit
	call := Call{Binding: binding, ProcessID: id}
	if err := server.JSON(t.Context(), "process.wait", call, &exit); err != nil {
		t.Fatal(err)
	}
	if exit.Code != 0 || exit.Error == "" {
		t.Fatalf("late collection failure lost its status: %+v", exit)
	}
	if err := server.JSON(t.Context(), "process.release", call, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonStreamedStartPreservesOutputUnderTransportBackpressure(t *testing.T) {
	d, server, binding := slowLinkDaemon(t, 128<<10)
	content := bytes.Repeat([]byte("synthetic output row\n"), 32<<10)
	path := filepath.Join(binding.CWD, "synthetic-output.txt")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	command := "cat '" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
	if runtime.GOOS == "windows" {
		command = "[Console]::Write([IO.File]::ReadAllText('" + strings.ReplaceAll(path, "'", "''") + "'))"
	}
	call := Call{Binding: binding, Execution: "synthetic_execution", APIToken: securityToken('s'),
		Process: standardtools.ProcessRequest{Command: command}}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	metadata, stream, err := server.Call(ctx, ProcessStreamCapability, call, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var started processStart
	if err := json.Unmarshal(metadata, &started); err != nil || started.ID == "" {
		t.Fatalf("streamed start identity: %s, %v", metadata, err)
	}
	d.mu.Lock()
	process := d.processes[started.ID]
	d.mu.Unlock()
	select {
	case <-process.done:
	case <-time.After(5 * time.Second):
		t.Fatal("local command/output capture waited for the rate-limited receiver")
	}
	raw, err := io.ReadAll(stream)
	if err != nil || !bytes.Equal(raw, content) {
		t.Fatalf("rate-limited output bytes=%d want=%d error=%v", len(raw), len(content), err)
	}
	call.ProcessID = started.ID
	var exit processExit
	if err := server.JSON(ctx, "process.wait", call, &exit); err != nil || exit.Code != 0 || exit.Error != "" {
		t.Fatalf("streamed status: %+v, %v", exit, err)
	}
	if err := server.JSON(ctx, "process.release", call, nil); err != nil {
		t.Fatal(err)
	}
	d.outputBudget.mu.Lock()
	remaining := d.outputBudget.used
	d.outputBudget.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("consumed output retained %d bytes of buffer budget", remaining)
	}
}

func TestRemoteReadRejectsIncompleteSuccessfulResponse(t *testing.T) {
	for _, failure := range []string{"short", "interrupted", "negative-size"} {
		t.Run(failure, func(t *testing.T) {
			hub, binding, _ := commandAuthFixture(t, func(_ context.Context, _ transport.Request, out *transport.Responder) error {
				size := int64(100)
				if failure == "negative-size" {
					size = -1
				}
				if err := out.Headers(FileInfo{Size: size}); err != nil {
					return err
				}
				if _, err := io.WriteString(out, "partial"); err != nil {
					return err
				}
				if failure == "interrupted" {
					return errors.New("synthetic transfer interrupted")
				}
				return nil
			})
			raw, err := (&remoteFS{hub: hub, binding: binding}).ReadFile(t.Context(), "synthetic.txt")
			if err == nil || len(raw) != 0 {
				t.Fatalf("incomplete read exposed bytes=%d error=%v", len(raw), err)
			}
		})
	}
}

func TestRemoteWriteRejectsOversizedContentsBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	hub, binding, _ := commandAuthFixture(t, func(context.Context, transport.Request, *transport.Responder) error {
		calls.Add(1)
		return nil
	})
	err := (&remoteFS{hub: hub, binding: binding}).WriteFile(t.Context(), "synthetic.txt", make([]byte, fileWriteSizeLimit+1))
	if err == nil || calls.Load() != 0 {
		t.Fatalf("oversized write used %d RPCs: %v", calls.Load(), err)
	}
}

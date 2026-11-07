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
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/domain"
	testshell "github.com/oines/astral/internal/testkit/shell"
	"github.com/oines/astral/internal/transport"
)

// Hold the actual response bytes after a process has started. Cancellation can
// still travel in the other direction, and no process ID has reached the client.
type processOutputHeaderGate struct {
	net.Conn
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
	close   sync.Once
}

func (g *processOutputHeaderGate) Write(data []byte) (int, error) {
	var frame struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(data, &frame) == nil && frame.Kind == "headers" {
		g.once.Do(func() {
			close(g.entered)
			select {
			case <-g.release:
			case <-g.closed:
			}
		})
	}
	return g.Conn.Write(data)
}

func (g *processOutputHeaderGate) Close() error {
	g.close.Do(func() { close(g.closed) })
	return g.Conn.Close()
}

type processOutputCleanupFixture struct {
	daemon            *Daemon
	peer              *transport.Peer
	binding           domain.ExecutionBinding
	generation        *daemonConnection
	gate              *processOutputHeaderGate
	handlerDone       chan error
	allowCleanupError bool
}

func newProcessOutputCleanupFixture(t *testing.T, gateHeaders bool) *processOutputCleanupFixture {
	t.Helper()
	d, err := NewDaemon(t.TempDir(), Credentials{ServerURL: "http://127.0.0.1:4545", ComputerID: "synthetic_output_cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	d.Hello.Home = t.TempDir()
	f := &processOutputCleanupFixture{daemon: d, binding: securityBinding(t, d, "synthetic_cleanup_grant"), handlerDone: make(chan error, 1)}
	left, right := net.Pipe()
	var daemonConn net.Conn = left
	if gateHeaders {
		f.gate = &processOutputHeaderGate{Conn: left, entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
		daemonConn = f.gate
	}
	f.generation = &daemonConnection{executions: map[string]string{}}
	f.generation.peer = transport.New(daemonConn, func(ctx context.Context, request transport.Request, out *transport.Responder) error {
		err := d.handle(ctx, f.generation, request, out)
		if request.Method == ProcessStreamCapability {
			f.handlerDone <- err
		}
		return err
	})
	d.mu.Lock()
	d.connection, d.peer = f.generation, f.generation.peer
	d.mu.Unlock()
	f.peer = transport.New(right, nil)
	t.Cleanup(func() {
		_ = f.peer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.Close(ctx); err != nil && !f.allowCleanupError {
			t.Errorf("close synthetic output cleanup fixture: %v", err)
		}
	})
	return f
}

func waitProcessOutputCleanup(t *testing.T, condition func() bool) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("synthetic process cleanup did not settle")
		}
	}
}

func TestDaemonCancelledOutputReapsProcessAndKeepsExecutionIdentity(t *testing.T) {
	for _, beforeHeaders := range []bool{true, false} {
		for _, tty := range []bool{false, true} {
			t.Run("before_headers_"+strconv.FormatBool(beforeHeaders)+"_tty_"+strconv.FormatBool(tty), func(t *testing.T) {
				f := newProcessOutputCleanupFixture(t, beforeHeaders)
				const marker = "synthetic output before cancellation"
				call := Call{Binding: f.binding, Execution: "synthetic_cancelled_execution", APIToken: securityToken('c'),
					Process: standardtools.ProcessRequest{Command: testshell.Print(marker) + "; " + testshell.Sleep(30*time.Second), TTY: tty}}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if beforeHeaders {
					started := make(chan error, 1)
					go func() {
						_, output, err := f.peer.Call(ctx, ProcessStreamCapability, call, nil)
						if output != nil {
							_ = output.Close()
						}
						started <- err
					}()
					processOutputTestWait(t, f.gate.entered)
					waitProcessOutputCleanup(t, func() bool { return processOutputTestUsage(f.daemon.outputBudget) > 0 })
					cancel()
					select {
					case err := <-started:
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("startup did not cancel before its ID arrived: %v", err)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("cancelled startup remained blocked on response headers")
					}
				} else {
					metadata, output, err := f.peer.Call(ctx, ProcessStreamCapability, call, nil)
					if err != nil {
						t.Fatal(err)
					}
					var started processStart
					if err := json.Unmarshal(metadata, &started); err != nil || started.ID == "" {
						t.Fatalf("streaming startup metadata: %s %v", metadata, err)
					}
					actual := make([]byte, len(marker))
					if _, err := io.ReadFull(output, actual); err != nil || string(actual) != marker {
						t.Fatalf("real process did not produce its synthetic output: %q %v", actual, err)
					}
					cancel()
					_ = output.Close()
				}
				// This must finish even while the response header is still gated.
				// The client cannot send terminate/release without knowing its ID.
				waitProcessOutputCleanup(t, func() bool {
					f.daemon.mu.Lock()
					defer f.daemon.mu.Unlock()
					return len(f.daemon.processes) == 0 && len(f.daemon.apiTokens) == 0
				})
				if used := processOutputTestUsage(f.daemon.outputBudget); used != 0 {
					t.Fatalf("cancelled output retained %d bytes of quota", used)
				}
				f.daemon.mu.Lock()
				id := f.generation.executions[call.Execution]
				f.daemon.mu.Unlock()
				if id == "" {
					t.Fatal("cancelled execution lost its deduplication tombstone")
				}
				if f.gate != nil {
					close(f.gate.release)
				}
				select {
				case err := <-f.handlerDone:
					if err == nil {
						t.Fatal("cancelled output was reported as successful")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("cancelled Daemon output handler did not return")
				}
				_, output, err := f.peer.Call(t.Context(), ProcessStreamCapability, call, nil)
				if output != nil {
					_ = output.Close()
				}
				var wire *transport.Error
				if !errors.As(err, &wire) || wire.Code != "already_dispatched" || wire.Outcome != "unknown" {
					t.Fatalf("cancelled execution could be replayed: %v", err)
				}
			})
		}
	}
}

func TestDaemonReleaseRetainsRecordWhenSpoolCleanupFails(t *testing.T) {
	f := newProcessOutputCleanupFixture(t, false)
	// Give a real short-lived process enough output to create a disk segment.
	data := bytes.Repeat([]byte("synthetic retained output\n"), 6000)
	path := filepath.Join(f.binding.CWD, "synthetic-cleanup-output.txt")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	command := "cat " + shellQuote(path)
	if f.daemon.Hello.OS == "windows" {
		command = "[Console]::Write([IO.File]::ReadAllText(" + powerShellQuote(path) + "))"
	}
	call := Call{Binding: f.binding, Execution: "synthetic_release_cleanup_failure", APIToken: securityToken('r'), Process: standardtools.ProcessRequest{Command: command}}
	var started processStart
	if err := f.peer.JSON(t.Context(), "process.start", call, &started); err != nil {
		t.Fatal(err)
	}
	f.daemon.mu.Lock()
	p := f.daemon.processes[started.ID]
	f.daemon.mu.Unlock()
	processOutputTestWait(t, p.done)
	p.output.mu.Lock()
	var spool string
	for _, segment := range p.output.segments {
		if segment.path != "" {
			spool = segment.path
			break
		}
	}
	p.output.mu.Unlock()
	if spool == "" {
		t.Fatal("synthetic process did not create a spool segment")
	}
	if err := os.Remove(spool); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(spool, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "synthetic-cleanup-blocker"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	// A nonempty directory makes Remove fail on every supported OS. The test's
	// private TempDir owns the deliberately unremovable path after this assertion.
	f.allowCleanupError = true
	call.ProcessID = started.ID
	if err := f.peer.JSON(t.Context(), "process.release", call, nil); err == nil {
		t.Fatal("release swallowed its spool cleanup error")
	}
	f.daemon.mu.Lock()
	retained := f.daemon.processes[started.ID] == p && f.generation.executions[call.Execution] == started.ID
	f.daemon.mu.Unlock()
	if !retained || processOutputTestUsage(f.daemon.outputBudget) == 0 {
		t.Fatal("failed release discarded its record or unremoved storage quota")
	}
}

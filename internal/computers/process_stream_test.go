package computers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/transport"
)

func advertiseProcessStream(hub *Hub, active *connection) {
	hub.mu.Lock()
	active.hello.Capabilities = append(active.hello.Capabilities, ProcessStreamCapability)
	hub.mu.Unlock()
}

func processStreamInvocation(binding domain.ExecutionBinding) core.Invocation {
	return core.Invocation{SessionID: binding.SessionID, ExecutionID: "synthetic-stream-execution", Binding: BindingJSON(binding)}
}

func assertNoProcessStreamCredentials(t *testing.T, hub *Hub) {
	t.Helper()
	hub.mu.RLock()
	remaining := len(hub.commandCredentials)
	hub.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("process startup retained %d command credentials", remaining)
	}
}

func cleanupProcessStream(t *testing.T, process standardtools.Process) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = process.Terminate(ctx)
	})
}

func TestProcessStartUsesAdvertisedStreamingCapability(t *testing.T) {
	for _, test := range []struct {
		name      string
		streaming bool
		disabled  bool
	}{
		{name: "legacy"},
		{name: "streaming", streaming: true},
		{name: "legacy with API disabled", disabled: true},
		{name: "streaming with API disabled", streaming: true, disabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			var methods []string
			released := make(chan struct{})
			hub, binding, active := commandAuthFixture(t, func(_ context.Context, request transport.Request, out *transport.Responder) error {
				mu.Lock()
				methods = append(methods, request.Method)
				mu.Unlock()
				switch request.Method {
				case ProcessStreamCapability, "process.start":
					var call Call
					if err := json.Unmarshal(request.Params, &call); err != nil {
						return err
					}
					if call.Execution != "synthetic-stream-execution" || !validAPIToken(call.APIToken) || call.Process.Command != "synthetic command" {
						return errors.New("startup lost execution identity, credential or process request")
					}
					if request.Method == "process.start" {
						return out.JSON(processStart{ID: "synthetic-process"})
					}
					if err := out.Headers(processStart{ID: "synthetic-process"}); err != nil {
						return err
					}
					_, err := io.WriteString(out, "synthetic process output\n")
					return err
				case "process.output":
					if err := out.Headers(struct{}{}); err != nil {
						return err
					}
					_, err := io.WriteString(out, "synthetic process output\n")
					return err
				case "process.wait":
					return out.JSON(processExit{Code: 7})
				case "process.release":
					close(released)
					return out.JSON(struct{}{})
				default:
					return errors.New("unexpected synthetic process operation")
				}
			})
			if test.streaming {
				advertiseProcessStream(hub, active)
			}
			if test.disabled {
				var computer domain.Computer
				if err := hub.Store.Get(t.Context(), "computers", binding.ComputerID, &computer); err != nil {
					t.Fatal(err)
				}
				if _, err := hub.UpdateComputer(t.Context(), computer.ID, computer.Revision, nil, &test.disabled); err != nil {
					t.Fatal(err)
				}
			}
			process, err := (&Backend{Hub: hub}).StartProcess(t.Context(), processStreamInvocation(binding), standardtools.ProcessRequest{Command: "synthetic command"})
			if err != nil {
				t.Fatal(err)
			}
			if test.disabled {
				assertNoProcessStreamCredentials(t, hub)
			}
			output, err := io.ReadAll(process.Output())
			if err != nil || string(output) != "synthetic process output\n" {
				t.Fatalf("startup stream output: %q error=%v", output, err)
			}
			_ = process.Output().Close()
			code, err := process.Wait()
			if err != nil || code != 7 {
				t.Fatalf("startup stream wait: code=%d error=%v", code, err)
			}
			select {
			case <-released:
			case <-time.After(3 * time.Second):
				t.Fatal("completed process did not release its remote state")
			}
			mu.Lock()
			observed := append([]string(nil), methods...)
			mu.Unlock()
			wanted := []string{"process.start", "process.output", "process.wait", "process.release"}
			if test.streaming {
				wanted = []string{ProcessStreamCapability, "process.wait", "process.release"}
			}
			if !slices.Equal(observed, wanted) {
				t.Fatalf("startup RPC sequence: got=%v want=%v", observed, wanted)
			}
			assertNoProcessStreamCredentials(t, hub)
		})
	}
}

func TestRejectedProcessStartStreamDoesNotRetryLegacyStartup(t *testing.T) {
	var streamCalls, legacyCalls atomic.Int32
	hub, binding, active := commandAuthFixture(t, func(_ context.Context, request transport.Request, _ *transport.Responder) error {
		if request.Method == ProcessStreamCapability {
			streamCalls.Add(1)
			return &transport.Error{Code: "unsupported", Message: "synthetic streaming rejection", Outcome: "not_executed"}
		}
		legacyCalls.Add(1)
		return errors.New("legacy startup must not replay a rejected streaming operation")
	})
	advertiseProcessStream(hub, active)
	process, err := (&Backend{Hub: hub}).StartProcess(t.Context(), processStreamInvocation(binding), standardtools.ProcessRequest{Command: "synthetic command"})
	var tool *core.ToolError
	if process != nil || !errors.As(err, &tool) || tool.Outcome != core.OutcomeNotExecuted {
		t.Fatalf("rejected stream outcome: process=%v error=%v", process, err)
	}
	if streamCalls.Load() != 1 || legacyCalls.Load() != 0 {
		t.Fatalf("rejected stream was retried: streaming=%d legacy=%d", streamCalls.Load(), legacyCalls.Load())
	}
	assertNoProcessStreamCredentials(t, hub)
}

func TestProcessStartStreamCancellationBeforeHeadersRevokesCredential(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	hub, binding, active := commandAuthFixture(t, func(ctx context.Context, request transport.Request, _ *transport.Responder) error {
		if request.Method != ProcessStreamCapability {
			return errors.New("cancelled streaming startup dispatched another operation")
		}
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	})
	advertiseProcessStream(hub, active)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		process, err := (&Backend{Hub: hub}).StartProcess(ctx, processStreamInvocation(binding), standardtools.ProcessRequest{Command: "synthetic command"})
		if process != nil {
			done <- errors.New("cancelled startup returned a live process")
			return
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("streaming startup did not reach its header gate")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled streaming startup: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled streaming startup did not return")
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("startup cancellation did not reach the remote handler")
	}
	assertNoProcessStreamCredentials(t, hub)
}

func TestProcessStartStreamSurvivesCallerCancellationAfterHeaders(t *testing.T) {
	issued := make(chan string, 1)
	release, finished := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var terminated atomic.Int32
	hub, binding, active := commandAuthFixture(t, func(ctx context.Context, request transport.Request, out *transport.Responder) error {
		switch request.Method {
		case ProcessStreamCapability:
			var call Call
			if err := json.Unmarshal(request.Params, &call); err != nil {
				return err
			}
			issued <- call.APIToken
			if err := out.Headers(processStart{ID: "synthetic-running-process"}); err != nil {
				return err
			}
			select {
			case <-release:
				_, err := io.WriteString(out, "output after caller cancellation\n")
				close(finished)
				return err
			case <-ctx.Done():
				close(finished)
				return ctx.Err()
			}
		case "process.wait":
			<-finished
			return out.JSON(processExit{Code: 0})
		case "process.terminate":
			terminated.Add(1)
			return out.JSON(struct{}{})
		case "process.release":
			return out.JSON(struct{}{})
		default:
			return errors.New("unexpected operation after streaming startup")
		}
	})
	advertiseProcessStream(hub, active)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	process, err := (&Backend{Hub: hub}).StartProcess(ctx, processStreamInvocation(binding), standardtools.ProcessRequest{Command: "synthetic command", TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	cleanupProcessStream(t, process)
	cancel()
	_, _, releaseCredential, err := hub.commandRequest(t.Context(), active, GatewayRequest{Binding: binding, APIToken: <-issued})
	if err != nil {
		t.Fatalf("ending the invocation revoked a yielded command: %v", err)
	}
	releaseCredential()
	releaseOnce.Do(func() { close(release) })
	output, err := io.ReadAll(process.Output())
	if err != nil || string(output) != "output after caller cancellation\n" {
		t.Fatalf("caller cancellation ended the yielded stream: %q %v", output, err)
	}
	_ = process.Output().Close()
	if code, err := process.Wait(); err != nil || code != 0 {
		t.Fatalf("yielded stream completion: code=%d error=%v", code, err)
	}
	if terminated.Load() != 0 {
		t.Fatal("caller cancellation terminated the yielded command")
	}
}

func TestProcessWriteDelayedAcknowledgementDoesNotReplayInput(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "acknowledged"
		if cancelled {
			name = "cancelled-before-ack"
		}
		t.Run(name, func(t *testing.T) {
			delivered, acknowledge := make(chan string, 1), make(chan struct{})
			var acknowledgeOnce sync.Once
			t.Cleanup(func() { acknowledgeOnce.Do(func() { close(acknowledge) }) })
			var writes atomic.Int32
			hub, binding, active := commandAuthFixture(t, func(ctx context.Context, request transport.Request, out *transport.Responder) error {
				switch request.Method {
				case ProcessStreamCapability:
					if err := out.Headers(processStart{ID: "synthetic-interactive-process"}); err != nil {
						return err
					}
					<-ctx.Done()
					return ctx.Err()
				case "process.write":
					input, err := io.ReadAll(request.Body)
					if err != nil {
						return err
					}
					writes.Add(1)
					delivered <- string(input)
					select {
					case <-acknowledge:
						return out.JSON(struct{}{})
					case <-ctx.Done():
						return ctx.Err()
					}
				case "process.terminate", "process.release":
					return out.JSON(struct{}{})
				default:
					return errors.New("unexpected synthetic stdin operation")
				}
			})
			advertiseProcessStream(hub, active)
			process, err := (&Backend{Hub: hub}).StartProcess(t.Context(), processStreamInvocation(binding), standardtools.ProcessRequest{Command: "synthetic command", TTY: true})
			if err != nil {
				t.Fatal(err)
			}
			cleanupProcessStream(t, process)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- process.Write(ctx, []byte("synthetic stdin payload\n")) }()
			select {
			case input := <-delivered:
				if input != "synthetic stdin payload\n" {
					t.Fatalf("stdin payload changed: %q", input)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stdin was not delivered")
			}
			select {
			case err := <-done:
				t.Fatalf("write returned before remote acknowledgement: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			if cancelled {
				cancel()
			} else {
				acknowledgeOnce.Do(func() { close(acknowledge) })
			}
			select {
			case err := <-done:
				if cancelled && !errors.Is(err, context.Canceled) || !cancelled && err != nil {
					t.Fatalf("stdin acknowledgement outcome: %v", err)
				}
				if cancelled {
					var tool *core.ToolError
					if !errors.As(err, &tool) || tool.Outcome != core.OutcomeUnknown {
						t.Fatalf("cancellation after delivery claimed the input was not executed: %v", err)
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stdin write did not settle")
			}
			if writes.Load() != 1 {
				t.Fatalf("stdin was replayed while its acknowledgement was delayed: %d deliveries", writes.Load())
			}
		})
	}
}

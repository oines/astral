package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	testshell "github.com/oines/astral/internal/testkit/shell"
	"github.com/oines/astral/internal/transport"
)

// Use a subprocess because the production failure occurs outside net/http's
// recovery boundary. A recover in this test would conceal a process-fatal panic.
func TestDeleteSubagentDisconnectedProcessDoesNotKillService(t *testing.T) {
	const childEnvironment = "ASTRAL_TEST_RUNTIME_CLOSE_FATAL_REPLAY"
	const survived = "runtime-close replay: background StorageBusy survived"
	if os.Getenv(childEnvironment) == "1" {
		replayDeleteSubagentDisconnectedProcess(t)
		fmt.Println(survived)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDeleteSubagentDisconnectedProcessDoesNotKillService$", "-test.v", "-test.timeout=45s")
	command.Env = append(os.Environ(), childEnvironment+"=1")
	output, err := command.CombinedOutput()
	if err != nil {
		code := -1
		if command.ProcessState != nil {
			code = command.ProcessState.ExitCode()
		}
		t.Fatalf("deleting a disconnected subagent process killed the service subprocess (exit %d): %v\n%s", code, err, output)
	}
	if !bytes.Contains(output, []byte(survived)) {
		t.Fatalf("replay did not reach the background reader:\n%s", output)
	}
	t.Logf("subprocess survived real process cleanup failure:\n%s", output)
}

func replayDeleteSubagentDisconnectedProcess(t *testing.T) {
	t.Helper()
	var childCalls atomic.Int32
	f := newFixture(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "You are a subagent of Session") && childCalls.Add(1) == 1 {
			return toolReply("retained-remote-process", "exec_command", map[string]any{
				"cmd": testshell.Sleep(time.Minute), "login": false, "yield_time_ms": 250,
			})
		}
		return textReply("The delegated Run has finished; its yielded process remains owned by the tool set.")
	})
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "fatal-replay-child", "Start the retained process for the deletion replay.", false)
	subagentTestFinished(t, f, child.SessionID)
	f.idle(t, parent.id)
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	r.processMu.Lock()
	processes := len(r.processes)
	r.processMu.Unlock()
	if childCalls.Load() < 2 || processes != 1 {
		t.Fatalf("the real child Run did not yield a live remote process: model calls=%d processes=%d", childCalls.Load(), processes)
	}
	session := r.core.Load()
	subagentRecoveryStopWorker(f)
	// This closes the original authenticated peer, not a fake returned error.
	// The Daemon also reaps its local process on connection loss.
	f.hub.Forget(f.daemons[0].Credentials.ComputerID)
	if err = f.daemons[0].Close(f.ctx); err != nil {
		t.Logf("close disconnected Daemon after reaping its process: %v", err)
	}
	if err = f.service.deleteSubagent(f.ctx, parent.id, child.SessionID); !errors.Is(err, transport.ErrDisconnected) {
		t.Fatalf("expected the real disconnected-process cleanup error, got %v", err)
	}
	if state := session.State(); state.SessionID != "" {
		t.Fatalf("Core.Close had not completed before tool cleanup failed: %+v", state)
	}
	var retained domain.Session
	if err = f.store.Get(f.ctx, "sessions", child.SessionID, &retained); err != nil || retained.Deletion == nil {
		t.Fatalf("cleanup failure lost the durable deletion intent: %+v, %v", retained, err)
	}
	fmt.Println("runtime-close replay: real child exec_command; peer disconnected; Core closed; tool cleanup failed; deletion retained")
	read := make(chan bool, 1)
	go func() { read <- f.service.StorageBusy() }()
	select {
	case busy := <-read:
		if busy {
			t.Fatal("a completely closed failed-deletion runtime still reports active Core work")
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if _, err = f.service.controlSubagent(f.ctx, parent.id, "retry-disconnected-child-deletion", AgentControlInput{AgentID: child.SessionID, Signal: "delete"}); err != nil {
		t.Fatal("the original parent could not retry real process cleanup failure:", err)
	}
	var deleted domain.Session
	if err = f.store.Get(f.ctx, "sessions", child.SessionID, &deleted); !domain.IsMissing(err) {
		t.Fatalf("successful retry retained the child Host record: %+v, %v", deleted, err)
	}
	if _, err = f.coreStore.LoadState(f.ctx, child.SessionID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("successful retry retained the child Core session:", err)
	}
	fmt.Println("runtime-close replay: original parent retried deletion; child Host and Core records removed")
}

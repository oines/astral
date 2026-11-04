package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
)

func recoveryLargeToolHistory(t testing.TB, bytes, observations int) (*recoveryBatchMemory, []IMWakeup, core.ModelAttempt) {
	t.Helper()
	content := core.Text(strings.Repeat("Synthetic archived read result. ", bytes/len("Synthetic archived read result. ")+1))
	// A valid synthetic PNG exercises inline image bodies without private data.
	content = append(content, core.Content{Type: "image", MediaType: "image/png", Data: json.RawMessage(`"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLbtAAAAABJRU5ErkJggg=="`)})
	tool := core.ToolFunc{Spec: core.ToolDefinition{Name: "read", Version: "synthetic", Parameters: json.RawMessage(`{"type":"object"}`)}, Run: func(context.Context, core.Invocation, func(core.ToolProgress)) (core.ToolExecution, error) {
		return core.ToolExecution{Result: &core.ToolResult{Content: content, Outcome: core.OutcomeSucceeded}}, nil
	}}
	first := true
	f := newRecoveryBatchMemory(t, func(_ context.Context, _ core.Request) (core.Reply, error) {
		message := core.Message{Role: core.RoleAssistant, Content: core.Text("Handled the observations and image."), FinishReason: core.FinishStop}
		if first {
			first = false
			message.Content = []core.Content{{Type: "tool_call", ToolCall: &core.ToolCall{ID: "synthetic-read", Name: "read", Arguments: json.RawMessage(`{}`)}}}
			message.FinishReason = core.FinishToolCalls
		}
		return core.Reply{Message: message}, nil
	}, tool)
	inputs := make([]IMWakeup, observations)
	for at := range inputs {
		inputs[at] = recoveryBatchObservation(t, f.ctx, f.session, "recovery-chat", fmt.Sprintf("archived-%03d", at))
	}
	recoveryRun(t, f, "historical-read")
	attempt := recoveryBatchAttempt(t, f.ctx, f.session, len(inputs))
	return f, inputs, attempt
}

func recoveryRun(t testing.TB, f *recoveryBatchMemory, id string) {
	t.Helper()
	if _, err := f.session.Steer(f.ctx, core.Input{ID: id, Message: core.User("Handle all visible observations.")}); err != nil {
		t.Fatal(err)
	}
	run, err := f.session.Continue(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Wait(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func recoveryAuditPages(t testing.TB, f *recoveryBatchMemory, inputs []IMWakeup, resetEachPage bool, width int) {
	t.Helper()
	for at := 0; at < len(inputs); at += width {
		if resetEachPage {
			f.service.ReleaseIMRecovery(f.session.ID())
		}
		page := inputs[at:min(at+width, len(inputs))]
		statuses, err := f.service.WakeObservations(f.ctx, f.session.ID(), page, "recovery-binding")
		if err != nil || len(statuses) != len(page) {
			t.Fatalf("recover page %d: statuses=%v err=%v", at/width, statuses, err)
		}
		for _, status := range statuses {
			if status != IMWakeCovered {
				t.Fatalf("historical observation was replayed: %s", status)
			}
		}
	}
}

func TestIMRecoveryAuditSurvivesPagesButNotCoreReplacementOrCatchup(t *testing.T) {
	f, inputs, attempt := recoveryLargeToolHistory(t, 64<<10, 21)
	f.store.watch(attempt)
	before, calls := f.session.State().Version, f.calls.Load()
	recoveryAuditPages(t, f, inputs, false, 7)
	if got := f.store.measured(); got != (recoveryBatchReads{attempts: 1, heads: 1, paths: 1}) {
		t.Fatalf("three recovered pages rebuilt the same request: %+v", got)
	}
	if f.calls.Load() != calls || f.session.State().Version != before {
		t.Fatal("auditing historical coverage ran the model or changed history")
	}
	// A cached negative decision would hide a new attempt. Existing immutable
	// evidence may stay cached while current receipt/attempt discovery changes.
	fresh := recoveryBatchObservation(t, f.ctx, f.session, "recovery-chat", "new-after-cached-history")
	r := f.service.runtimes[f.session.ID()]
	if handled, err := r.imInspector.Handled(f.ctx, fresh.InputID); err != nil || handled {
		t.Fatalf("old history falsely covered new input: handled=%v err=%v", handled, err)
	}
	recoveryRun(t, f, "handle-new-observation")
	newAttempt := recoveryBatchAttempt(t, f.ctx, f.session, len(inputs)+1)
	f.store.watch(newAttempt)
	recoveryAuditPages(t, f, []IMWakeup{fresh}, false, 7)
	if f.store.measured().attempts != 1 {
		t.Fatal("latest attempt was hidden by cached historical evidence")
	}
	previous := r.imInspector
	if err := f.session.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := core.OpenSession(f.ctx, f.config, f.session.ID())
	if err != nil {
		t.Fatal(err)
	}
	f.session = reopened
	r.core.Store(reopened)
	recoveryAuditPages(t, f, []IMWakeup{fresh}, false, 7)
	if r.imInspector == previous || r.imInspector.session != reopened {
		t.Fatal("reopened Core retained the previous inspector")
	}
	f.service.ReleaseIMRecovery(f.session.ID())
	if r.imInspector != nil {
		t.Fatal("completed catchup retained historical audit evidence")
	}
	cold := &Service{runtimes: map[string]*runtime{}}
	cold.ReleaseIMRecovery("unknown-cold-session")
	if len(cold.runtimes) != 0 {
		t.Fatal("releasing evidence opened a cold Session")
	}
}

// Run explicitly for bounded, synthetic reproduction of the production hot
// path: immutable requests include a large tool result and inline image.
func TestIMRecoveryLargeFrozenRequestCrossPageMeasurement(t *testing.T) {
	if os.Getenv("ASTRAL_IM_RECOVERY_BENCH") != "1" {
		t.Skip("explicit bounded frozen-request recovery measurement")
	}
	f, inputs, attempt := recoveryLargeToolHistory(t, 16<<20, 305)
	request, err := f.session.Attempts().Request(f.ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	requestBytes := len(encode(request))
	results := []map[string]any{}
	for _, legacy := range []bool{true, false} {
		f.service.ReleaseIMRecovery(f.session.ID())
		f.store.watch(attempt)
		goruntime.GC()
		goruntime.GC() // discard encoding/json sync.Pool victim buffers too
		var before, after goruntime.MemStats
		goruntime.ReadMemStats(&before)
		started := time.Now()
		recoveryAuditPages(t, f, inputs, legacy, 100)
		elapsed := time.Since(started)
		goruntime.GC()
		goruntime.GC()
		goruntime.ReadMemStats(&after)
		reads := f.store.measured()
		want := 1
		if legacy {
			want = 4
		}
		if reads.attempts != want || reads.heads != want || reads.paths != want {
			t.Fatalf("real frozen request rebuilds=%+v, want %d", reads, want)
		}
		var evidenceBytes int
		for id, audit := range f.service.runtimes[f.session.ID()].imInspector.audits {
			evidenceBytes += len(id)
			for event := range audit.events {
				evidenceBytes += len(event)
			}
		}
		results = append(results, map[string]any{"legacy_page_cache": legacy, "observations": len(inputs), "pages": 4, "request_bytes": requestBytes, "retained_audit_key_bytes": evidenceBytes,
			"elapsed_ns": elapsed.Nanoseconds(), "rebuilds": reads.attempts, "total_alloc_bytes": after.TotalAlloc - before.TotalAlloc,
			"live_heap_delta_bytes": int64(after.HeapAlloc) - int64(before.HeapAlloc), "model_calls": f.calls.Load()})
	}
	bytes, _ := json.MarshalIndent(results, "", "  ")
	t.Log(string(bytes))
	if output := os.Getenv("ASTRAL_IM_RECOVERY_RESULT"); output != "" {
		if err := os.WriteFile(output, bytes, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.service.ReleaseIMRecovery(f.session.ID())
}

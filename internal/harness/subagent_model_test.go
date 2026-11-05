package harness

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	testshell "github.com/oines/astral/internal/testkit/shell"
)

func subagentTreeModel(t *testing.T, f *fixture) domain.ModelProfile {
	t.Helper()
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Meta, profile.Name, profile.ModelID = domain.Meta{}, "Tree model", "fixture-tree"
	profile.Recommendations["reasoning"] = encode(map[string]any{"efforts": []string{"low", "high"}})
	declareFixtureModelRecommendations(t, f, &profile)
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	return profile
}

func subagentWaitTreeSelection(t *testing.T, f *fixture, ids []string, wanted core.ModelSelection) {
	t.Helper()
	eventually(t, f.ctx, func() bool {
		for _, id := range ids {
			selection, err := f.service.ModelSelection(f.ctx, id)
			if err != nil || selection.ID != wanted.ID || !reflect.DeepEqual(selection.Reasoning, wanted.Reasoning) {
				return false
			}
			var record domain.Session
			if err := f.store.Get(f.ctx, "sessions", id, &record); err != nil {
				return false
			}
			if record.SubagentParentID != "" {
				var settings subagentSettings
				if err := f.store.Get(f.ctx, "subagent_settings", id, &settings); err != nil || settings.Configuration.Selection.ID != wanted.ID || !reflect.DeepEqual(settings.Configuration.Selection.Reasoning, wanted.Reasoning) {
					return false
				}
			}
		}
		return true
	})
}

func subagentSelectTreeModel(t *testing.T, f *fixture, id string, wanted core.ModelSelection) {
	t.Helper()
	eventually(t, f.ctx, func() bool {
		var err error
		if wanted.Reasoning == nil {
			err = f.service.SelectModel(f.ctx, id, wanted.ID)
		} else {
			err = f.service.SelectModel(f.ctx, id, wanted.ID, wanted.Reasoning)
		}
		if errors.Is(err, core.ErrBusy) {
			return false // This root operation was not accepted; retry admission.
		}
		if err != nil {
			t.Fatal(err)
		}
		return true
	})
}

func TestComposerModelAndReasoningSelectionPropagatesOnlyToDelegatedTree(t *testing.T) {
	var queuedWire atomic.Value
	queuedWire.Store("")
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		raw := string(encode(request))
		if strings.Contains(raw, "You are a subagent of Session") && strings.Contains(raw, "QUEUED_TREE_MODEL_MARKER") {
			model, _ := request["model"].(string)
			reasoning, _ := request["reasoning_effort"].(string)
			queuedWire.Store(model + "|" + reasoning)
		}
		return textReply("Selected model result.")
	})
	parent := subagentTestSeed(t, f)
	explicit := anotherModel(t, f)
	target := subagentTreeModel(t, f)
	shared := subagentTestSpawn(t, f, parent, "sync-shared", "Shared-context work.", true)
	subagentTestFinished(t, f, shared.SessionID)
	independent, err := f.service.spawnSubagent(f.ctx, parent, core.Invocation{SessionID: parent.id, ExecutionID: "sync-explicit"}, SpawnAgentInput{Task: "Independent work with an explicit model.", Model: explicit.ID})
	if err != nil {
		t.Fatal(err)
	}
	subagentTestFinished(t, f, independent.SessionID)
	sharedRuntime, err := f.service.open(f.ctx, shared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	grandchild := subagentTestLegacySpawn(t, f, sharedRuntime, "sync-grandchild", "Existing recursive delegated work.", false)
	subagentTestFinished(t, f, grandchild.SessionID)
	outside, err := f.service.CreateSession(f.ctx, f.agent.ID, "Another ordinary root")
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, parent.id)
	ordinaryFork, err := f.service.Fork(f.ctx, parent.id, parent.core.Load().Head(), "User-created fork", domain.ID("session"))
	if err != nil {
		t.Fatal(err)
	}
	outsideSelection := sessionSelection(t, f, outside.ID)
	forkSelection := sessionSelection(t, f, ordinaryFork.ID)
	if selected := sessionSelection(t, f, independent.SessionID); selected.ID != explicit.ID {
		t.Fatal("fixture did not start with an explicit subagent model")
	}
	subagentRecoveryStopWorker(f)
	for _, id := range []string{parent.id, shared.SessionID, independent.SessionID, grandchild.SessionID} {
		f.idle(t, id)
	}
	queued := subagentTestSpawn(t, f, parent, "sync-queued", "QUEUED_TREE_MODEL_MARKER", false)
	if _, err := f.coreStore.LoadState(f.ctx, queued.SessionID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("queued fixture was already initialized: %v", err)
	}
	wanted := core.ModelSelection{ID: target.ID, Reasoning: &core.ReasoningOptions{Effort: "high"}}
	subagentSelectTreeModel(t, f, parent.id, wanted)
	var queuedSettings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", queued.SessionID, &queuedSettings); err != nil || queuedSettings.Configuration.Selection.ID != target.ID || !reflect.DeepEqual(queuedSettings.Configuration.Selection.Reasoning, wanted.Reasoning) {
		t.Fatalf("accepted root selection did not persist queued child configuration: %+v %v", queuedSettings.Configuration.Selection, err)
	}
	f.restart(t)
	subagentTestFinished(t, f, queued.SessionID)
	ids := []string{parent.id, shared.SessionID, independent.SessionID, grandchild.SessionID, queued.SessionID}
	subagentWaitTreeSelection(t, f, ids, wanted)
	if wire := queuedWire.Load().(string); wire != target.ModelID+"|high" {
		t.Fatalf("queued child's first request used stale model/reasoning: %q", wire)
	}
	if selected := sessionSelection(t, f, outside.ID); !reflect.DeepEqual(selected, outsideSelection) {
		t.Fatalf("root selection changed another normal root: before=%+v after=%+v", outsideSelection, selected)
	}
	if selected := sessionSelection(t, f, ordinaryFork.ID); !reflect.DeepEqual(selected, forkSelection) {
		t.Fatalf("root selection followed ordinary Fork provenance: before=%+v after=%+v", forkSelection, selected)
	}
	// Reasoning changes alone must propagate even though the model ID stays
	// the same, and reopening must not reload an earlier subagent override.
	wanted.Reasoning = &core.ReasoningOptions{Effort: "low"}
	subagentSelectTreeModel(t, f, parent.id, wanted)
	subagentWaitTreeSelection(t, f, ids, wanted)
	f.restart(t)
	subagentWaitTreeSelection(t, f, ids, wanted)
	before := map[string]core.ModelSelection{}
	for _, id := range ids {
		before[id] = sessionSelection(t, f, id)
	}
	for _, invalid := range []core.ModelSelection{
		{ID: "missing-tree-model"},
		{ID: target.ID, Reasoning: &core.ReasoningOptions{Effort: "unsupported"}},
	} {
		if err := f.service.SelectModel(f.ctx, parent.id, invalid.ID, invalid.Reasoning); err == nil {
			t.Fatalf("accepted invalid tree selection: %+v", invalid)
		}
		for _, id := range ids {
			if selected := sessionSelection(t, f, id); !reflect.DeepEqual(selected, before[id]) {
				t.Fatalf("invalid selection partially changed branch %s: before=%+v after=%+v", id, before[id], selected)
			}
		}
		subagentWaitTreeSelection(t, f, ids, wanted)
	}
	wanted.Reasoning = nil
	subagentSelectTreeModel(t, f, parent.id, wanted)
	subagentWaitTreeSelection(t, f, ids, wanted)
}

func TestComposerModelSelectionPreservesFrozenChildAttemptAndChangesNextRequest(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var childCalls atomic.Int32
	second := make(chan string, 1)
	f := newFixture(t, 1, func(ctx context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encode(request)), "You are a subagent of Session") {
			if childCalls.Add(1) == 1 {
				close(entered)
				select {
				case <-release:
					return toolReply("next-boundary", "exec_command", map[string]any{"cmd": testshell.CWD(), "login": false})
				case <-ctx.Done():
					return nil
				}
			}
			model, _ := request["model"].(string)
			reasoning, _ := request["reasoning_effort"].(string)
			second <- model + "|" + reasoning
		}
		return textReply("Work using the selected model.")
	})
	parent := subagentTestSeed(t, f)
	parentFrozen := subagentTestRequest(t, f, parent.id)
	target := subagentTreeModel(t, f)
	child := subagentTestSpawn(t, f, parent, "sync-running-child", "Use a tool, then report its result.", true)
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	frozen := subagentTestRequest(t, f, child.SessionID)
	if frozen.ModelID != parentFrozen.ModelID {
		t.Fatalf("fixture's active request did not use the original model: %s", frozen.ModelID)
	}
	wanted := core.ModelSelection{ID: target.ID, Reasoning: &core.ReasoningOptions{Effort: "high"}}
	subagentSelectTreeModel(t, f, parent.id, wanted)
	subagentWaitTreeSelection(t, f, []string{parent.id, child.SessionID}, wanted)
	stillFrozen := subagentTestRequest(t, f, child.SessionID)
	if !reflect.DeepEqual(stillFrozen, frozen) || childCalls.Load() != 1 {
		t.Fatal("Composer selection modified or restarted the in-flight frozen request")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case actual := <-second:
		if actual != target.ModelID+"|high" {
			t.Fatalf("next child request ignored selected model/reasoning: %q", actual)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	subagentTestFinished(t, f, child.SessionID)
	if childCalls.Load() != 2 {
		t.Fatalf("model selection created unexpected child attempts: %d", childCalls.Load())
	}
}

func TestHostSubagentModelSelectionPropagatesOnlyBelowSelectedSession(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Subtree model result.") })
	parent := subagentTestSeed(t, f)
	selected := subagentTestSpawn(t, f, parent, "selected-model-subtree", "Selected child.", true)
	subagentTestFinished(t, f, selected.SessionID)
	selectedRuntime, err := f.service.open(f.ctx, selected.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	grandchild := subagentTestLegacySpawn(t, f, selectedRuntime, "selected-model-grandchild", "Existing nested model target.", false)
	subagentTestFinished(t, f, grandchild.SessionID)
	sibling := subagentTestSpawn(t, f, parent, "unselected-model-sibling", "Sibling outside the selected subtree.", false)
	subagentTestFinished(t, f, sibling.SessionID)
	rootBefore := sessionSelection(t, f, parent.id)
	siblingBefore := sessionSelection(t, f, sibling.SessionID)
	target := subagentTreeModel(t, f)
	wanted := core.ModelSelection{ID: target.ID, Reasoning: &core.ReasoningOptions{Effort: "high"}}
	subagentSelectTreeModel(t, f, selected.SessionID, wanted)
	subagentWaitTreeSelection(t, f, []string{selected.SessionID, grandchild.SessionID}, wanted)
	if got := sessionSelection(t, f, parent.id); !reflect.DeepEqual(got, rootBefore) {
		t.Fatalf("child model selection propagated upward: before=%+v after=%+v", rootBefore, got)
	}
	if got := sessionSelection(t, f, sibling.SessionID); !reflect.DeepEqual(got, siblingBefore) {
		t.Fatalf("child model selection changed a sibling: before=%+v after=%+v", siblingBefore, got)
	}
}

func TestSubagentModelChangeRecoveryRequiresRootSelectionCommit(t *testing.T) {
	for _, rootCommitted := range []bool{false, true} {
		name := "intent-before-root-write"
		if rootCommitted {
			name = "root-write-before-child-settings"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Model intent result.") })
			parent := subagentTestSeed(t, f)
			child := subagentTestSpawn(t, f, parent, "model-intent-child", "Keep the original model before recovery.", false)
			state := subagentTestFinished(t, f, child.SessionID)
			eventually(t, f.ctx, func() bool {
				var checkpoint subagentCheckpoint
				return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
			})
			f.idle(t, parent.id)
			subagentRecoveryStopWorker(f)
			original := sessionSelection(t, f, parent.id)
			target := subagentTreeModel(t, f)
			wanted := core.ModelSelection{ID: target.ID, Reasoning: &core.ReasoningOptions{Effort: "high"}}
			change := subagentModelChange{RootID: parent.id, Selection: wanted, SessionIDs: []string{parent.id, child.SessionID}}
			if err := f.store.SaveMaintenance(f.ctx, subagentModelChangeKind, parent.id, "running", change); err != nil {
				t.Fatal(err)
			}
			if rootCommitted {
				// Bypass the Host fan-out to stop exactly after the first durable
				// Core write, before any child settings are changed.
				if err := parent.core.Load().SelectModel(f.ctx, wanted); err != nil {
					t.Fatal(err)
				}
			}
			var before subagentSettings
			if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &before); err != nil || before.Configuration.Selection.ID != original.ID {
				t.Fatalf("fixture propagated child settings before the crash: %+v %v", before.Configuration.Selection, err)
			}
			f.restart(t)
			expected := original
			if rootCommitted {
				expected = wanted
			}
			subagentWaitTreeSelection(t, f, []string{parent.id, child.SessionID}, expected)
			eventually(t, f.ctx, func() bool {
				var pending subagentModelChange
				return domain.IsMissing(f.store.Maintenance(f.ctx, subagentModelChangeKind, parent.id, &pending))
			})
			// Rechecking after acknowledgement also proves the uncommitted
			// intent was discarded rather than being applied on a later poll.
			if err := f.service.reconcileSubagentModelChanges(f.ctx); err != nil {
				t.Fatal(err)
			}
			subagentWaitTreeSelection(t, f, []string{parent.id, child.SessionID}, expected)
		})
	}
}

func TestSubagentCompactionBypassesTreeLockAndKeepsFrozenSummaryModel(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce, unlockOnce sync.Once
	var compactCalls atomic.Int32
	var nextGeneration atomic.Value
	nextGeneration.Store("")
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if _, tools := request["tools"]; !tools {
			if compactCalls.Add(1) == 1 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil
				}
			}
			return textReply("Retained compacted task details.")
		}
		raw := string(encode(request))
		if strings.Contains(raw, "You are a subagent of Session") && strings.Contains(raw, "AFTER_COMPACT_MODEL_SELECTION") {
			model, _ := request["model"].(string)
			reasoning, _ := request["reasoning_effort"].(string)
			nextGeneration.Store(model + "|" + reasoning)
		}
		return textReply("Normal generation result.")
	})
	parent := subagentTestSeed(t, f)
	parentRequest := subagentTestRequest(t, f, parent.id)
	child := subagentTestSpawn(t, f, parent, "compact-lock-child", strings.Repeat("Historical delegated work for a real compaction. ", 2500), false)
	state := subagentTestFinished(t, f, child.SessionID)
	eventually(t, f.ctx, func() bool {
		var checkpoint subagentCheckpoint
		return f.store.Maintenance(f.ctx, "subagent_delivery", child.SessionID, &checkpoint) == nil && checkpoint.Version == state.Version
	})
	subagentRecoveryStopWorker(f)
	session, err := f.service.Core(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	target := subagentTreeModel(t, f)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	var operation sync.WaitGroup
	f.service.subagentMu.Lock()
	unlockTree := func() { unlockOnce.Do(f.service.subagentMu.Unlock) }
	t.Cleanup(func() {
		unlockTree()
		releaseOnce.Do(func() { close(release) })
		cancel()
		joined := make(chan struct{})
		go func() { operation.Wait(); close(joined) }()
		select {
		case <-joined:
		case <-time.After(3 * time.Second):
			t.Error("bounded compaction operation did not exit during cleanup")
		}
	})
	finished := make(chan error, 1)
	operation.Add(1)
	go func() {
		defer operation.Done()
		_, err := session.Compact(ctx, "Retain the task before changing its model.")
		finished <- err
	}()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("compaction did not reach its provider while the tree lock was held: %v", err)
	case <-ctx.Done():
		t.Fatal("compaction re-entered the externally held tree lock:", ctx.Err())
	}
	page, err := session.Attempts().Scan(f.ctx, core.AttemptQuery{Purpose: "compaction", Reverse: true, Limit: 1, MaxBytes: 128 << 20})
	if err != nil || len(page.Attempts) != 1 {
		t.Fatalf("no real frozen compaction Attempt: %+v %v", page, err)
	}
	attemptID := page.Attempts[0].ID
	frozen, err := session.Attempts().Request(f.ctx, attemptID)
	if err != nil || frozen.ModelID != parentRequest.ModelID {
		t.Fatalf("compaction did not freeze the original model: %+v %v", frozen, err)
	}
	var settings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &settings); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(frozen.SystemPrompt, commonSystemPrompt) {
		t.Fatal("fixture did not use the auxiliary compaction System prompt")
	}
	// Simulate a desired selection already committed by the Host while Core
	// is busy summarizing. It must affect the next normal generation only.
	settings.Configuration.Selection = core.ModelSelection{ID: target.ID, Reasoning: &core.ReasoningOptions{Effort: "high"}}
	if err := f.store.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("compaction could not complete under the external tree lock:", ctx.Err())
	}
	// Keep the lock held through successful completion. A failed lock attempt
	// confirms that neither the test nor an auxiliary Stream released it.
	if f.service.subagentMu.TryLock() {
		f.service.subagentMu.Unlock()
		t.Fatal("fixture stopped holding the tree lock before compaction completed")
	}
	after, err := session.Attempts().Request(f.ctx, attemptID)
	if err != nil || !reflect.DeepEqual(after, frozen) {
		t.Fatalf("new desired model rewrote the frozen summary request: %v", err)
	}
	view, err := session.ReadContext(f.ctx)
	if err != nil || len(view.Request.Messages) == 0 || view.Request.Messages[0].ContextKind != "summary" {
		t.Fatalf("fixture did not complete a real compaction: %+v %v", view, err)
	}
	unlockTree()
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "after-compact-model", AgentControlInput{AgentID: child.SessionID, Input: "AFTER_COMPACT_MODEL_SELECTION"}); err != nil {
		t.Fatal(err)
	}
	subagentRecoveryReceipt(t, f, child.SessionID, "subagent_instruction_after-compact-model")
	subagentTestFinished(t, f, child.SessionID)
	if got := nextGeneration.Load().(string); got != target.ModelID+"|high" {
		t.Fatalf("post-compaction generation did not apply the desired model: %q", got)
	}
	afterGeneration, err := session.Attempts().Request(f.ctx, attemptID)
	if err != nil || !reflect.DeepEqual(afterGeneration, frozen) {
		t.Fatalf("later generation changed the earlier frozen summary request: %v", err)
	}
}

package harness

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestSubagentDepthAllowsRootChildrenAndRetainsLegacyDescendants(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Depth result.") })
	root := subagentTestSeed(t, f)
	first := subagentTestSpawn(t, f, root, "depth-first", "First sibling.", true)
	second := subagentTestSpawn(t, f, root, "depth-second", "Second sibling.", false)
	subagentTestFinished(t, f, first.SessionID)
	subagentTestFinished(t, f, second.SessionID)
	child, err := f.service.open(f.ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	grandchild := subagentTestLegacySpawn(t, f, child, "depth-grandchild", "Existing second delegation level.", false)
	subagentTestFinished(t, f, grandchild.SessionID)
	grand, err := f.service.open(f.ctx, grandchild.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{root.id, first.SessionID, second.SessionID, grandchild.SessionID} {
		f.idle(t, id)
	}
	// A user Fork carries provenance, but starts a separate delegation tree.
	fork, err := f.service.Fork(f.ctx, root.id, root.core.Load().Head(), "Independent ordinary fork", "ordinary-depth-root")
	if err != nil {
		t.Fatal(err)
	}
	if fork.SubagentParentID != "" || fork.ParentSessionID != root.id {
		t.Fatalf("wrong ordinary fork identity: %+v", fork)
	}
	forkRoot, err := f.service.open(f.ctx, fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	forkChild := subagentTestSpawn(t, f, forkRoot, "fork-depth-first", "New first level.", true)
	subagentTestFinished(t, f, forkChild.SessionID)
	forkChildRuntime, err := f.service.open(f.ctx, forkChild.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	forkGrand := subagentTestLegacySpawn(t, f, forkChildRuntime, "fork-depth-second", "Existing second level.", false)
	subagentTestFinished(t, f, forkGrand.SessionID)
	subagentRecoveryStopWorker(f)
	for _, id := range []string{root.id, first.SessionID, grand.id, fork.ID, forkChild.SessionID, forkGrand.SessionID} {
		f.idle(t, id)
	}
	ids := func(kind string) []string {
		t.Helper()
		rows, err := f.store.List(f.ctx, kind)
		if err != nil {
			t.Fatal(err)
		}
		result := make([]string, 0, len(rows))
		for _, row := range rows {
			result = append(result, row.ID)
		}
		sort.Strings(result)
		return result
	}
	beforeSessions, beforeSettings := ids("sessions"), ids("subagent_settings")
	for _, caller := range []*runtime{child, grand, forkChildRuntime} {
		beforeCore, err := f.coreStore.LoadState(f.ctx, caller.id)
		if err != nil {
			t.Fatal(err)
		}
		for _, inherited := range []bool{true, false} {
			execution := fmt.Sprintf("depth-rejected-%v", inherited)
			_, err := f.service.spawnSubagent(f.ctx, caller, core.Invocation{SessionID: caller.id, ExecutionID: execution}, SpawnAgentInput{Task: "Forbidden recursive delegation.", InheritContext: inherited})
			if err == nil || !strings.Contains(err.Error(), "only root -> subagent") || !strings.Contains(err.Error(), "subagents cannot spawn") {
				t.Fatalf("subagent %s inherit=%v did not return the one-level error: %v", caller.id, inherited, err)
			}
			failedID := "session_subagent_" + digest([]string{caller.id, execution})[:32]
			if _, err := f.coreStore.LoadState(f.ctx, failedID); !errors.Is(err, core.ErrNotFound) {
				t.Fatalf("rejected child left Core state: %v", err)
			}
			if !reflect.DeepEqual(ids("sessions"), beforeSessions) || !reflect.DeepEqual(ids("subagent_settings"), beforeSettings) {
				t.Fatal("rejected depth left Host records")
			}
			afterCore, err := f.coreStore.LoadState(f.ctx, caller.id)
			if err != nil || !reflect.DeepEqual(beforeCore, afterCore) {
				t.Fatalf("rejected depth modified caller Core state: %v", err)
			}
		}
	}
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", forkGrand.SessionID, &record); err != nil || record.SubagentParentID != forkChild.SessionID {
		t.Fatalf("legacy descendant of an ordinary Fork was changed: %+v %v", record, err)
	}
}

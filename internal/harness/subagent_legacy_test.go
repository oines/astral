package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/recall"
)

// subagentTestLegacySpawn reconstructs a descendant accepted by an older Host.
// It writes only the old durable Host facts; the current worker still performs
// ordinary initialization, Fork, execution and delivery. New production spawn
// admission is never bypassed by changing its depth limit or calling it here.
func subagentTestLegacySpawn(t *testing.T, f *fixture, parent *runtime, execution, task string, inherited bool) SubagentView {
	t.Helper()
	s := f.service
	if err := lockSubagentContext(f.ctx, &s.subagentMu); err != nil {
		t.Fatal(err)
	}
	defer s.subagentMu.Unlock()
	var source domain.Session
	if err := f.store.Get(f.ctx, "sessions", parent.id, &source); err != nil {
		t.Fatal(err)
	}
	if source.SubagentParentID == "" {
		t.Fatal("legacy descendant fixture requires a subagent parent")
	}
	id := "session_subagent_" + digest([]string{parent.id, execution})[:32]
	var existing domain.Session
	if err := f.store.Get(f.ctx, "sessions", id, &existing); err == nil {
		view, err := s.subagentView(f.ctx, existing)
		if err != nil {
			t.Fatal(err)
		}
		return view
	} else if !domain.IsMissing(err) {
		t.Fatal(err)
	}
	configuration, err := core.ReadConfiguration(f.ctx, f.coreStore, parent.id)
	if err != nil {
		t.Fatal(err)
	}
	var parentSettings subagentSettings
	if err := f.store.Get(f.ctx, "subagent_settings", parent.id, &parentSettings); err != nil {
		t.Fatal(err)
	}
	configuration.Selection = parentSettings.Configuration.Selection
	configuration.SystemPrompt, err = parent.currentSystemPrompt(f.ctx, configuration.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	settings := subagentSettings{Meta: domain.Meta{ID: id}, Configuration: subagentConfiguration{Selection: configuration.Selection},
		InheritContext: inherited, ForkEntryID: parent.core.Load().Head(), Task: task}
	if inherited && settings.ForkEntryID == "" {
		t.Fatal("legacy shared descendant requires committed parent history")
	}
	record := domain.Session{Meta: domain.Meta{ID: id}, AgentID: source.AgentID, Title: "Legacy " + execution, TitleSource: domain.TitleManual,
		Source: "subagent", SubagentParentID: source.ID, ComputerID: source.ComputerID, CWD: source.CWD, Private: source.Private}
	if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error {
		if err := tx.Save(f.ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
			return err
		}
		return tx.Save(f.ctx, "sessions", &record.Meta, &record)
	}); err != nil {
		t.Fatal(err)
	}
	s.notify(id)
	s.wakeSubagents()
	return SubagentView{SessionID: id, ParentSessionID: parent.id, Title: record.Title, Name: record.Title, Selection: configuration.Selection, Status: "queued"}
}

func TestLegacyGrandchildrenRemainReadableControllableAndFinishAfterDepthChange(t *testing.T) {
	for _, inherited := range []bool{true, false} {
		t.Run(fmt.Sprintf("inherit=%v", inherited), func(t *testing.T) {
			entered := make(chan struct{})
			var blocked atomic.Bool
			f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
				if strings.Contains(string(encode(request)), "LEGACY_STOP_ONCE") && blocked.CompareAndSwap(false, true) {
					close(entered)
					<-ctx.Done()
					return nil
				}
				return textReply("Legacy work was completed and reported.")
			})
			root := subagentTestSeed(t, f)
			private := true
			if err := f.service.SaveSessionPresentation(f.ctx, root.id, nil, nil, nil, &private); err != nil {
				t.Fatal(err)
			}
			child := subagentTestSpawn(t, f, root, "legacy-owner", "Keep the existing delegated work available.", true)
			subagentTestFinished(t, f, child.SessionID)
			parent, err := f.service.open(f.ctx, child.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			execution, task := "legacy-grandchild", "LEGACY_STOP_ONCE: preserve this accepted old instruction exactly."
			grand := subagentTestLegacySpawn(t, f, parent, execution, task, inherited)
			select {
			case <-entered:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			if _, err := f.service.controlSubagent(f.ctx, root.id, "wrong-generation", AgentControlInput{AgentID: grand.SessionID, Signal: "stop"}); err == nil {
				t.Fatal("root bypassed the legacy direct parent")
			}
			if _, err := f.service.controlSubagent(f.ctx, parent.id, "stop-legacy", AgentControlInput{AgentID: grand.SessionID, Signal: "stop"}); err != nil {
				t.Fatal(err)
			}
			eventually(t, f.ctx, func() bool {
				state, err := f.coreStore.LoadState(f.ctx, grand.SessionID)
				return err == nil && state.LastRun != nil && state.LastRun.Status == "cancelled"
			})
			if _, err := f.service.controlSubagent(f.ctx, parent.id, "finish-legacy", AgentControlInput{AgentID: grand.SessionID, Input: "Finish the accepted legacy work and report the outcome."}); err != nil {
				t.Fatal(err)
			}
			subagentRecoveryReceipt(t, f, grand.SessionID, "subagent_instruction_finish-legacy")
			finished := subagentTestFinished(t, f, grand.SessionID)
			subagentRecoveryReceipt(t, f, parent.id, "subagent_run_end_"+grand.SessionID+"_"+finished.LastRun.ID)
			f.idle(t, parent.id)
			f.idle(t, root.id)
			for _, entry := range subagentTestEntries(t, f, root.id) {
				if entry.Message == nil || entry.Message.ContextKind != subagentUpdateKind {
					continue
				}
				var source map[string]string
				if jsonErr := json.Unmarshal(entry.Message.Metadata, &source); jsonErr == nil && source["child_session_id"] == grand.SessionID {
					t.Fatal("legacy grandchild completion skipped its direct parent")
				}
			}
			f.restart(t)
			rootRead := persistence.WithSessionScope(f.ctx, f.agent.ID, root.id)
			if page, err := f.service.Recall.Read(rootRead, f.agent.ID, grand.SessionID, recall.ReadRequest{}); err != nil || len(page.Items) == 0 {
				t.Fatalf("private legacy descendant history became unreadable: %+v %v", page, err)
			}
			result, err := f.service.controlSubagent(f.ctx, parent.id, "query-legacy", AgentControlInput{AgentID: grand.SessionID})
			if err != nil || subagentPublicView(t, result).Name != grand.Name {
				t.Fatalf("legacy descendant cannot be managed after reopening: %+v %v", result, err)
			}
			parent, err = f.service.open(f.ctx, parent.id)
			if err != nil {
				t.Fatal(err)
			}
			// Replay of an already accepted old tool execution is not a new
			// delegation. Recovery must return the same existing Session.
			again, err := f.service.spawnSubagent(f.ctx, parent, core.Invocation{SessionID: parent.id, ExecutionID: execution}, SpawnAgentInput{Task: task, InheritContext: inherited})
			if err != nil || again.SessionID != grand.SessionID {
				t.Fatalf("old accepted spawn could not be replayed idempotently: %+v %v", again, err)
			}
			for _, inherit := range []bool{true, false} {
				if _, err := f.service.spawnSubagent(f.ctx, parent, core.Invocation{SessionID: parent.id, ExecutionID: fmt.Sprintf("new-after-upgrade-%v", inherit)}, SpawnAgentInput{Task: "New recursive work is forbidden.", InheritContext: inherit}); err == nil {
					t.Fatal("a legacy parent created new recursive work")
				}
			}
			if _, err := f.service.controlSubagent(f.ctx, parent.id, "delete-legacy", AgentControlInput{AgentID: grand.SessionID, Signal: "delete"}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.coreStore.LoadState(f.ctx, grand.SessionID); !errors.Is(err, core.ErrNotFound) {
				t.Fatalf("legacy descendant could not be cleaned up: %v", err)
			}
			for _, id := range []string{root.id, child.SessionID} {
				var record domain.Session
				if err := f.store.Get(f.ctx, "sessions", id, &record); err != nil {
					t.Fatalf("legacy cleanup removed its surviving ancestor %s: %v", id, err)
				}
			}
		})
	}
}

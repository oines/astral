package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestDeleteAgentRemovesDelegatedTreesAcrossRolloutsAndKeepsFilesAndOtherBots(t *testing.T) {
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any { return textReply("Result retained until deletion.") })
	root := subagentTestSeed(t, f)
	binding, err := root.currentBinding(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	workingFile := filepath.Join(binding.CWD, "keep-working-file.txt")
	if err := os.WriteFile(workingFile, []byte("working files are outside Session history"), 0600); err != nil {
		t.Fatal(err)
	}
	shared := subagentTestSpawn(t, f, root, "bot-shared-child", "Shared-context delegated work.", true)
	sharedState := subagentTestFinished(t, f, shared.SessionID)
	sharedRuntime, err := f.service.open(f.ctx, shared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	grandchild := subagentTestLegacySpawn(t, f, sharedRuntime, "bot-fresh-grandchild", "Fresh-context grandchild work.", false)
	grandchildState := subagentTestFinished(t, f, grandchild.SessionID)
	if grandchildState.RolloutID == sharedState.RolloutID {
		t.Fatal("fixture did not create a descendant in an independent Rollout")
	}
	secondRoot, err := f.service.CreateSession(f.ctx, f.agent.ID, "Another normal root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, secondRoot.ID, InputRequest{ID: "second-root-seed", Text: "Separate user-facing work."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, secondRoot.ID)
	secondRuntime, err := f.service.open(f.ctx, secondRoot.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondChild := subagentTestSpawn(t, f, secondRuntime, "second-shared-child", "Shared child of another normal root.", true)
	secondState := subagentTestFinished(t, f, secondChild.SessionID)
	if secondState.RolloutID == sharedState.RolloutID {
		t.Fatal("separate normal roots unexpectedly shared a Rollout")
	}
	other := f.agent
	other.Meta, other.Name = domain.Meta{}, "Unaffected Bot"
	if err := f.service.SaveAgent(f.ctx, &other); err != nil {
		t.Fatal(err)
	}
	otherSession, err := f.service.CreateSession(f.ctx, other.ID, "Unaffected history")
	if err != nil {
		t.Fatal(err)
	}
	otherBefore, err := f.coreStore.LoadState(f.ctx, otherSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err != nil {
		t.Fatal("idempotent Bot deletion:", err)
	}
	for _, id := range []string{root.id, shared.SessionID, grandchild.SessionID, secondRoot.ID, secondChild.SessionID} {
		var record domain.Session
		if err := f.store.Get(f.ctx, "sessions", id, &record); !domain.IsMissing(err) {
			t.Fatalf("Bot deletion retained delegated Host Session %s: %v", id, err)
		}
		if _, err := f.coreStore.LoadState(f.ctx, id); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("Bot deletion retained delegated Core Session %s: %v", id, err)
		}
		var settings subagentSettings
		if err := f.store.Get(f.ctx, "subagent_settings", id, &settings); !domain.IsMissing(err) {
			t.Fatalf("Bot deletion retained subagent settings %s: %v", id, err)
		}
	}
	var deleted domain.Agent
	if err := f.store.Get(f.ctx, "agents", f.agent.ID, &deleted); !domain.IsMissing(err) {
		t.Fatalf("deleted Bot record remains: %v", err)
	}
	var retained domain.Agent
	if err := f.store.Get(f.ctx, "agents", other.ID, &retained); err != nil || retained.Deletion {
		t.Fatalf("other Bot was changed: %+v %v", retained, err)
	}
	otherAfter, err := f.coreStore.LoadState(f.ctx, otherSession.ID)
	if err != nil || otherBefore.Version != otherAfter.Version || otherBefore.RolloutID != otherAfter.RolloutID {
		t.Fatalf("other Bot's history was changed: before=%+v after=%+v error=%v", otherBefore, otherAfter, err)
	}
	content, err := os.ReadFile(workingFile)
	if err != nil || string(content) != "working files are outside Session history" {
		t.Fatalf("Bot deletion changed Computer work: content=%q error=%v", content, err)
	}
}

func TestDeleteAgentRejectsSharedHistoryAcrossDistinctDelegationRoots(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Root context.") })
	root := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, root, "owned-shared-child", "Legitimate delegated work.", true)
	subagentTestFinished(t, f, child.SessionID)
	state := root.core.Load().State()
	ordinary := domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: f.agent.ID, ParentSessionID: root.id, ForkEntryID: state.Branch.HeadID}
	if _, err := f.coreStore.Fork(f.ctx, core.ForkRequest{SourceID: root.id, SourceRolloutID: state.RolloutID, TargetID: ordinary.ID, HeadID: state.Branch.HeadID, Mode: core.ForkShared}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Save(f.ctx, "sessions", &ordinary.Meta, &ordinary); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err == nil {
		t.Fatal("ordinary Fork provenance was treated as delegated tree ownership")
	}
	var agent domain.Agent
	if err := f.store.Get(f.ctx, "agents", f.agent.ID, &agent); err != nil || agent.Deletion {
		t.Fatalf("rejected mixed sharing committed irreversible Bot intent: %+v %v", agent, err)
	}
}

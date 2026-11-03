package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

type subagentDeleteFaultStore struct {
	core.Store
	core.SessionDeleter
	branchDeleter core.BranchDeleter
	target        string
	after         bool
}

func (s *subagentDeleteFaultStore) DeleteBranch(ctx context.Context, request core.BranchDeleteRequest) (core.BranchDeleteResult, error) {
	if request.SessionID != s.target {
		return s.branchDeleter.DeleteBranch(ctx, request)
	}
	if s.after {
		if _, err := s.branchDeleter.DeleteBranch(ctx, request); err != nil {
			return core.BranchDeleteResult{}, err
		}
	}
	return core.BranchDeleteResult{}, errors.New("injected subagent tree deletion interruption")
}

func TestRootDeletionResumesWholeDelegatedTreeAfterCoreInterruption(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-Core=%v", after), func(t *testing.T) {
			f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Recorded delegated result.") })
			root := subagentTestSeed(t, f)
			shared := subagentTestSpawn(t, f, root, "recovery-shared", "Shared child.", true)
			subagentTestFinished(t, f, shared.SessionID)
			independent := subagentTestSpawn(t, f, root, "recovery-independent", "Independent child.", false)
			subagentTestFinished(t, f, independent.SessionID)
			independentRuntime, err := f.service.open(f.ctx, independent.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			legacy := subagentTestLegacySpawn(t, f, independentRuntime, "recovery-legacy-shared", "Legacy shared grandchild.", true)
			subagentTestFinished(t, f, legacy.SessionID)
			subagentRecoveryStopWorker(f)
			ids := []string{root.id, shared.SessionID, independent.SessionID, legacy.SessionID}
			rollouts := map[string]bool{}
			for _, id := range ids {
				f.idle(t, id)
				state, err := f.coreStore.LoadState(f.ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				rollouts[state.RolloutID] = true
			}
			record := archiveForDeletion(t, f, root.id)
			f.service.CoreStore = &subagentDeleteFaultStore{Store: f.coreStore, SessionDeleter: f.coreStore, branchDeleter: f.coreStore, target: independent.SessionID, after: after}
			if err := f.service.DeleteSession(f.ctx, record.ID, record.Revision); err == nil {
				t.Fatal("deletion interruption was not surfaced")
			}
			for _, id := range ids {
				var pending domain.Session
				err := f.store.Get(f.ctx, "sessions", id, &pending)
				if domain.IsMissing(err) {
					continue // A previous leaf may already have been fully purged.
				}
				if err != nil || pending.Deletion == nil || pending.Deletion.TreeRootID != root.id {
					t.Fatalf("tree deletion intent lost for %s: %+v %v", id, pending, err)
				}
				if _, err := f.service.View(f.ctx, id); !domain.IsMissing(err) {
					t.Fatalf("partially deleted Session %s can reopen: %v", id, err)
				}
			}
			requests := f.count()
			f.restart(t)
			if err := f.service.ResumeSessionDeletions(f.ctx); err != nil {
				t.Fatal("resume tree deletion:", err)
			}
			for _, id := range ids {
				var pending domain.Session
				if err := f.store.Get(f.ctx, "sessions", id, &pending); !domain.IsMissing(err) {
					t.Fatalf("recovery retained Host Session %s: %v", id, err)
				}
				if _, err := f.coreStore.LoadState(f.ctx, id); !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("recovery retained Core Session %s: %v", id, err)
				}
				var settings subagentSettings
				if err := f.store.Get(f.ctx, "subagent_settings", id, &settings); !domain.IsMissing(err) {
					t.Fatalf("recovery retained delegated settings %s: %v", id, err)
				}
			}
			for rollout := range rollouts {
				page, err := f.coreStore.Scan(f.ctx, rollout, core.Query{Limit: 1})
				if err != nil && !errors.Is(err, core.ErrNotFound) || len(page.Records) != 0 {
					t.Fatalf("recovery retained Rollout %s: %+v %v", rollout, page, err)
				}
			}
			if f.count() != requests {
				t.Fatal("deletion recovery invoked a model")
			}
		})
	}
}

type subagentDeleteGateStore struct {
	core.Store
	core.SessionDeleter
	core.BranchDeleter
	target  string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *subagentDeleteGateStore) LoadState(ctx context.Context, id string) (core.RuntimeState, error) {
	if id == s.target {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return core.RuntimeState{}, ctx.Err()
		}
	}
	return s.Store.LoadState(ctx, id)
}

func TestRootDeletionPreventsConcurrentSpawnAndStopsRunningDelegates(t *testing.T) {
	for _, runningChildren := range []bool{false, true} {
		t.Run(fmt.Sprintf("running-children=%v", runningChildren), func(t *testing.T) {
			started := make(chan struct{}, 2)
			f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
				if strings.Contains(string(encode(request)), "BLOCK_UNTIL_TREE_DELETION") {
					started <- struct{}{}
					<-ctx.Done()
					return nil
				}
				return textReply("Root ready.")
			})
			root := subagentTestSeed(t, f)
			ids := []string{root.id}
			if runningChildren {
				for _, inherited := range []bool{true, false} {
					child := subagentTestSpawn(t, f, root, fmt.Sprintf("running-%v", inherited), "BLOCK_UNTIL_TREE_DELETION", inherited)
					ids = append(ids, child.SessionID)
					select {
					case <-started:
					case <-f.ctx.Done():
						t.Fatal(f.ctx.Err())
					}
				}
			}
			subagentRecoveryStopWorker(f)
			record := archiveForDeletion(t, f, root.id)
			gate := &subagentDeleteGateStore{Store: f.coreStore, SessionDeleter: f.coreStore, BranchDeleter: f.coreStore, target: root.id, entered: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(gate.release) }) })
			f.service.CoreStore = gate
			deleted := make(chan error, 1)
			go func() { deleted <- f.service.DeleteSession(f.ctx, record.ID, record.Revision) }()
			select {
			case <-gate.entered:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			// The deletion has selected its range while holding subagentMu,
			// including the initially childless single-Session path.
			attempted, spawned := make(chan struct{}), make(chan error, 1)
			go func() {
				close(attempted)
				_, err := f.service.spawnSubagent(f.ctx, root, core.Invocation{SessionID: root.id, ExecutionID: "concurrent-with-delete"}, SpawnAgentInput{Task: "Must not escape root deletion."})
				spawned <- err
			}()
			<-attempted
			release.Do(func() { close(gate.release) })
			if err := <-deleted; err != nil {
				t.Fatal("root deletion:", err)
			}
			if err := <-spawned; err == nil {
				t.Fatal("concurrent Spawn escaped the selected deletion range")
			}
			failedID := "session_subagent_" + digest([]string{root.id, "concurrent-with-delete"})[:32]
			ids = append(ids, failedID)
			for _, id := range ids {
				var record domain.Session
				if err := f.store.Get(f.ctx, "sessions", id, &record); !domain.IsMissing(err) {
					t.Fatalf("deletion left Host Session %s: %+v %v", id, record, err)
				}
				if _, err := f.coreStore.LoadState(f.ctx, id); !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("deletion left Core Session %s: %v", id, err)
				}
				var settings subagentSettings
				if err := f.store.Get(f.ctx, "subagent_settings", id, &settings); !domain.IsMissing(err) {
					t.Fatalf("deletion left delegated settings %s: %v", id, err)
				}
			}
		})
	}
}

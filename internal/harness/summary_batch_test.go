package harness

import (
	"context"
	"strings"
	"sync"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestSummariesKeepColdHistoryAndRefreshSharedAvailability(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("directory lookup called the model")
		return nil
	})
	other, err := f.service.CreateSession(f.ctx, f.agent.ID, "Second directory entry")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{f.session.ID, other.ID} {
		session, err := f.service.Core(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := session.Steer(f.ctx, core.Input{ID: "large-queued-input", Message: core.User(strings.Repeat("Queued owner text. ", 8192))}); err != nil {
			t.Fatal(err)
		}
	}
	broken := domain.Session{Meta: domain.Meta{ID: "missing-agent-session"}, AgentID: "missing-agent", Title: "Unavailable history"}
	deleted := domain.Session{Meta: domain.Meta{ID: "deleted-session"}, AgentID: f.agent.ID, Deletion: &domain.SessionDeletion{}}
	for _, record := range []*domain.Session{&broken, &deleted} {
		if err := f.store.Save(f.ctx, "sessions", &record.Meta, record); err != nil {
			t.Fatal(err)
		}
	}
	f.restart(t)
	subagentRecoveryStopWorker(f)
	counted := &coldQueryStore{Store: f.coreStore}
	f.service.CoreStore = counted
	for range 2 {
		rows, err := f.service.Summaries(f.ctx)
		if err != nil || len(rows) != 3 {
			t.Fatalf("directory rows=%+v err=%v", rows, err)
		}
		for _, row := range rows {
			if row.ID == broken.ID {
				if row.Status != "needs_attention" || row.Error != "This Session could not be opened. Check its Agent and model configuration." {
					t.Fatalf("unavailable row lost its fallback: %+v", row)
				}
			} else if row.ID == deleted.ID || row.Status != "idle" || !row.ModelAvailable {
				t.Fatalf("valid or deleted directory row changed: %+v", row)
			}
		}
	}
	if counted.stateReads.Load() != 4 || counted.pendingReads.Load() != 0 || counted.pathReads.Load() != 0 || len(f.service.runtimes) != 0 || f.count() != 0 {
		t.Fatalf("batch directory opened execution data: states=%d pending=%d paths=%d runtimes=%d model_calls=%d", counted.stateReads.Load(), counted.pendingReads.Load(), counted.pathReads.Load(), len(f.service.runtimes), f.count())
	}
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	disabled := false
	profile.Enabled = &disabled
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	rows, err := f.service.Summaries(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID != broken.ID && (row.ModelAvailable || row.ModelUnavailableReason != "model_disabled" || row.Status != "needs_attention") {
			t.Fatalf("shared availability escaped its request: %+v", row)
		}
	}
}

func TestSummariesPreserveLiveRunningAndMaintenanceState(t *testing.T) {
	for _, operation := range []string{"running", "compacting"} {
		t.Run(operation, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			releaseModel := sync.OnceFunc(func() { close(release) })
			defer releaseModel()
			completed := make(chan error, 1)
			f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
				if operation == "compacting" && n <= 2 {
					return textReply("Completed before compaction.")
				}
				close(entered)
				select {
				case <-release:
					return textReply("Finished.")
				case <-ctx.Done():
					return nil
				}
			})
			text := "Run the local request."
			if operation == "compacting" {
				text = strings.Repeat("Retain this history when compacting. ", 2500)
			}
			if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "directory-live", Text: text}); err != nil {
				t.Fatal(err)
			}
			if operation == "compacting" {
				f.idle(t, f.session.ID)
				if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "directory-compact-history", Text: "Complete another turn so history can be compacted."}); err != nil {
					t.Fatal(err)
				}
				f.idle(t, f.session.ID)
				go func() { completed <- f.service.Compact(f.ctx, f.session.ID, "Summarize this history.") }()
			}
			select {
			case <-entered:
			case err := <-completed:
				t.Fatalf("compaction finished before its model request: %v", err)
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			rows, err := f.service.Summaries(f.ctx)
			if err != nil || len(rows) != 1 || rows[0].Status != operation || operation == "running" && rows[0].RunID == "" {
				t.Fatalf("live directory lost its Core state: rows=%+v err=%v", rows, err)
			}
			releaseModel()
			if operation == "compacting" {
				select {
				case err := <-completed:
					if err != nil {
						t.Fatal(err)
					}
				case <-f.ctx.Done():
					t.Fatal(f.ctx.Err())
				}
			} else {
				f.idle(t, f.session.ID)
			}
		})
	}
}

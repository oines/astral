package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

func TestInboxReadBudgetIsNotClassifiedAsPermanentModelFailure(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return nil })
	s := New(p.Store, p.Harness)
	b := activeDeliveryChat(t, p, s, false, false)
	boundary, err := s.inboxBoundary(p.Context, b)
	if err != nil {
		t.Fatal(err)
	}
	// A BudgetError also represents byte limits, and passive admission can leave
	// it pending. Only the explicit compaction sentinel adds a new pause policy.
	for _, cause := range []error{
		fmt.Errorf("history read: %w", core.ErrBudget),
		&core.BudgetError{Input: 2000, Output: 500, Margin: 100, Window: 2400},
		&core.BudgetError{Input: 8192, Window: 4096},
	} {
		if err = s.pauseInboxFailure(p.Context, b, boundary, cause); err != nil {
			t.Fatal(err)
		}
		var pause inboxPause
		if err = p.Store.Maintenance(p.Context, inboxPauseKind, b.ID, &pause); !domain.IsMissing(err) {
			t.Fatalf("unrelated budget failure %v became a permanent model pause: %v", cause, err)
		}
	}
}

func TestInboxCompactionBudgetPauseRetainsQueueAcrossRestartAndRecoversAfterModelRepair(t *testing.T) {
	// Use the same real Core/Host queue and offline model transport as the
	// quota-recovery workload. The smaller window still fits incoming observations
	// but rejects the old summary before any provider request can be made.
	const oldPending, whilePaused = 8, 3
	p, s, b, failed, calls := unavailableFixtureWithPending(t, oldPending)
	failed.Store(false)
	if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	p.Profile.Recommendations["context_window"] = testkit.JSON(16384)
	p.Profile.Recommendations["max_output_tokens"] = testkit.JSON(512)
	if err := p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
		t.Fatal(err)
	}
	if available, reason := p.Harness.Models.Availability(p.Context, p.Profile.ID); !available {
		t.Fatalf("fixture model is unavailable instead of input being too large: %s", reason)
	}
	first := fmt.Sprint(firstPendingBatch(t, p, b.SessionID))
	event, through := activeDeliveryArchive(t, p, b, "compaction-budget-failure", "")
	if err := s.deliver(p.Context, b, event); !errors.Is(err, core.ErrCompactionInputTooLarge) {
		t.Fatalf("actual Core did not reject the unfit summary input: %v", err)
	}
	assertPause := func() {
		t.Helper()
		var pause inboxPause
		if err := p.Store.Maintenance(p.Context, inboxPauseKind, b.ID, &pause); err != nil || !pause.RetryAt.IsZero() || pause.Error == "" {
			t.Fatalf("budget failure was not persistently paused without a timer: %+v %v", pause, err)
		}
		if paused, err := s.pausedInbox(p.Context, b); err != nil || !paused {
			t.Fatalf("model budget pause was not active: %v %v", paused, err)
		}
	}
	assertPause()
	for i := range whilePaused {
		later, sequence := activeDeliveryArchive(t, p, b, fmt.Sprintf("archived-during-budget-pause-%d", i), "")
		through = sequence
		if err := s.deliver(p.Context, b, later); err == nil {
			t.Fatal("new platform observation bypassed the budget pause")
		}
	}
	state, err := p.CoreStore.LoadState(p.Context, b.SessionID)
	queued := firstPendingBatch(t, p, b.SessionID)
	if err != nil || state.PendingInputs[core.InputContext] != oldPending+1 || len(queued) < oldPending || fmt.Sprint(queued[:oldPending]) != first || calls.Load() != 0 {
		t.Fatalf("budget pause changed queue identities or reached the provider: pending=%v calls=%d err=%v", state.PendingInputs, calls.Load(), err)
	}
	if reason, err := s.IMDeliveryError(p.Context, b.SessionID); err != nil || reason == "" {
		t.Fatalf("budget pause did not reach the existing error surface: %q %v", reason, err)
	}

	activeDeliveryReopen(t, p)
	s = New(p.Store, p.Harness)
	stop := runPausedInbox(t, p, s, b)
	defer stop()
	assertPause()
	// Restore the ordinary window while the inbox is waiting. The old history
	// still needs a summary, which must now fit and commit. The configuration
	// change lets the waiting inbox retry; no new chat message is sent.
	if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	p.Profile.Recommendations["context_window"] = testkit.JSON(32768)
	if err := p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
		t.Fatal(err)
	}
	testkit.Await(t, p.Context, func() bool {
		var at cursor
		if p.Store.Get(p.Context, "inbox_cursors", b.ID, &at) != nil || at.Sequence != through {
			return false
		}
		state, err := p.CoreStore.LoadState(p.Context, b.SessionID)
		return err == nil && state.PendingInputs[core.InputContext] == 0
	})
	session, err := p.Harness.Core(p.Context, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.History().Scan(p.Context, core.Query{Kind: core.EntryContext, Limit: 1000, MaxBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, entry := range page.Entries {
		if entry.Message == nil {
			continue
		}
		var metadata messages.Metadata
		if json.Unmarshal(entry.Message.Metadata, &metadata) == nil && metadata.Source == "im" {
			seen[metadata.EventID]++
		}
	}
	if len(seen) != oldPending+1+whilePaused {
		t.Fatalf("model repair lost queued/archived observations: %d", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("model repair duplicated observation %s: %d", id, count)
		}
	}
	if reason, err := s.IMDeliveryError(p.Context, b.SessionID); err != nil || reason != "" {
		t.Fatalf("model repair retained a stale pause: %q %v", reason, err)
	}
	compactions, err := session.History().Scan(p.Context, core.Query{Kind: core.EntryCompaction, Limit: 100, MaxBytes: 1 << 20})
	if err != nil || len(compactions.Entries) == 0 || calls.Load() == 0 {
		t.Fatalf("model repair did not perform and commit the required compaction: compactions=%d calls=%d err=%v", len(compactions.Entries), calls.Load(), err)
	}
	t.Logf("COMPACTION_BUDGET_RECOVERY pending_before=%d archived_during_pause=%d observations_applied=%d committed_compactions=%d model_calls_after_repair=%d", oldPending+1, whilePaused, len(seen), len(compactions.Entries), calls.Load())

	// A successful passive recovery must also leave ordinary IM execution usable.
	// The existing inbox receives this new mention and the offline provider returns
	// plain assistant text; no real platform send or new sender stub is involved.
	fresh, _ := activeDeliveryArchive(t, p, b, "mention-after-compaction-recovery", "mention")
	testkit.Await(t, p.Context, func() bool {
		receipt, err := session.LookupInput(p.Context, fresh.ID+":trigger")
		if err != nil || receipt.State != core.InputApplied {
			return false
		}
		state, err := p.CoreStore.LoadState(p.Context, b.SessionID)
		return err == nil && state.LastRun != nil && state.LastRun.Status == "completed"
	})
	ordinary, err := session.Attempts().Scan(p.Context, core.AttemptQuery{Purpose: "generation", Limit: 10, MaxBytes: 1 << 20})
	if err != nil || len(ordinary.Attempts) != 1 || ordinary.Attempts[0].Outcome == nil || ordinary.Attempts[0].Outcome.Status != "completed" {
		t.Fatalf("new mention did not finish exactly one ordinary generation after recovery: %+v %v", ordinary, err)
	}
	request, err := session.Attempts().Request(p.Context, ordinary.Attempts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range request.Messages {
		for _, part := range message.Content {
			found = found || strings.Contains(part.Text, fresh.Text)
		}
	}
	if !found {
		t.Fatal("recovered ordinary generation did not receive the new mention")
	}
}

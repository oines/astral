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
	// plain output text without send_message, so the existing missed-reply policy
	// also issues one independently identified reminder after the mention's Run.
	fresh, _ := activeDeliveryArchive(t, p, b, "mention-after-compaction-recovery", "mention")
	testkit.Await(t, p.Context, func() bool {
		receipt, err := session.LookupInput(p.Context, fresh.ID+":trigger")
		if err != nil || receipt.State != core.InputApplied {
			return false
		}
		state, err := p.CoreStore.LoadState(p.Context, b.SessionID)
		var replyWatch any
		return err == nil && state.LastRun != nil && state.LastRun.Status == "completed" && domain.IsMissing(p.Store.Maintenance(p.Context, "im_reply_watch", b.SessionID, &replyWatch))
	})
	state, err = p.CoreStore.LoadState(p.Context, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	consumed, err := p.CoreStore.Scan(p.Context, state.RolloutID, core.Query{SessionID: b.SessionID, Kind: core.EntryInputConsumed, Limit: 100, MaxBytes: 1 << 20})
	if err != nil || consumed.Next != "" {
		t.Fatalf("could not read the fixture's consumed input identities: next=%q error=%v", consumed.Next, err)
	}
	drivers := map[string][]core.InputReceipt{}
	for _, row := range consumed.Records {
		entry := row.Entry
		if entry.Input == nil || entry.Input.Kind == core.InputContext {
			continue
		}
		receipt, err := session.LookupInput(p.Context, entry.Input.ID)
		if err != nil {
			t.Fatal(err)
		}
		drivers[entry.RunID] = append(drivers[entry.RunID], receipt)
	}
	generated, err := session.Attempts().Scan(p.Context, core.AttemptQuery{Purpose: "generation", Limit: 10, MaxBytes: 1 << 20})
	if err != nil || generated.Next != "" {
		t.Fatalf("could not read the fixture's generation attempts: next=%q error=%v", generated.Next, err)
	}
	var ordinary, reminders []core.ModelAttempt
	var reminderID string
	for _, attempt := range generated.Attempts {
		if attempt.Outcome == nil || attempt.Outcome.Status != "completed" || len(drivers[attempt.RunID]) != 1 {
			t.Fatalf("generation %s did not complete with one driving input: run=%s drivers=%d", attempt.ID, attempt.RunID, len(drivers[attempt.RunID]))
		}
		driver := drivers[attempt.RunID][0]
		var source messages.Metadata
		if driver.Message == nil || json.Unmarshal(driver.Message.Metadata, &source) != nil {
			t.Fatalf("generation %s has no identified driving message", attempt.ID)
		}
		t.Logf("generation run=%s driver=%s source=%s context_kind=%s", attempt.RunID, driver.ID, source.Source, driver.Message.ContextKind)
		switch source.Source {
		case "im.trigger":
			if source.EventID != fresh.ID || driver.ID != fresh.ID+":trigger" && driver.ID != fresh.ID+":trigger:wakeup" {
				t.Fatalf("ordinary generation used a different mention: driver=%s event=%s", driver.ID, source.EventID)
			}
			ordinary = append(ordinary, attempt)
		case "im.reply_reminder":
			if driver.Message.ContextKind != "im.reply_reminder" || source.DeliveryID != driver.ID {
				t.Fatalf("reply reminder lost its source identity: driver=%s source=%+v", driver.ID, source)
			}
			reminders = append(reminders, attempt)
			reminderID = driver.ID
		default:
			t.Fatalf("unexpected generation source after model repair: driver=%s source=%s", driver.ID, source.Source)
		}
	}
	if len(ordinary) != 1 || len(reminders) != 1 || reminderID != "im_reply_reminder_"+ordinary[0].RunID {
		t.Fatalf("expected one mention generation and its one missed-reply reminder: ordinary=%d reminders=%d reminder_id=%s", len(ordinary), len(reminders), reminderID)
	}
	request, err := session.Attempts().Request(p.Context, ordinary[0].ID)
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

package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type opaqueActivityStore struct {
	core.Store
	tokens map[string]string
	used   []core.Query
}

func (s *opaqueActivityStore) Scan(ctx context.Context, rollout string, query core.Query) (core.Page, error) {
	if query.Kind != core.EntryRun {
		return s.Store.Scan(ctx, rollout, query)
	}
	s.used = append(s.used, query)
	if query.Cursor != "" {
		raw, ok := s.tokens[query.Cursor]
		if !ok {
			return core.Page{}, core.ErrInvalid
		}
		query.Cursor = raw
	}
	page, err := s.Store.Scan(ctx, rollout, query)
	if page.Next != "" {
		token := "opaque-run-page:" + base64.RawURLEncoding.EncodeToString([]byte(page.Next))
		s.tokens[token] = page.Next
		page.Next = token
	}
	return page, err
}

func TestSubagentActivityUsesOnlyPublicOpaqueRunCursors(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("A short response.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "opaque-activity", "Complete the first instruction.", false)
	state := subagentTestFinished(t, f, child.SessionID)
	subagentWorkerCheckpoint(t, f, child.SessionID, state)
	f.idle(t, parent.id)
	subagentRecoveryStopWorker(f)
	session, err := f.service.Core(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// More than 64 lifecycle facts force a public continuation token. The
	// runtime's configuration Store remains untouched by this read adapter.
	for i := range 32 {
		result, err := session.Prompt(f.ctx, core.Input{ID: fmt.Sprint("opaque-", i), Message: core.User("Finish this short instruction.")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := result.Run.Wait(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error {
		if err := tx.PurgeSubagentActivities(f.ctx, parent.id); err != nil {
			return err
		}
		return tx.DeleteMaintenance(f.ctx, subagentActivityCursorKind, child.SessionID)
	}); err != nil {
		t.Fatal(err)
	}
	store := &opaqueActivityStore{Store: f.coreStore, tokens: map[string]string{}}
	f.service.CoreStore = store
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	project := func() {
		t.Helper()
		for {
			state := session.State()
			more, err := f.service.recordSubagentActivities(f.ctx, record, state)
			if err != nil {
				t.Fatal("projection did not use a public Store cursor:", err)
			}
			if !more {
				break
			}
		}
	}
	project()
	if len(store.tokens) == 0 || len(store.used) < 2 || store.used[0].Cursor != "" || !strings.HasPrefix(store.used[1].Cursor, "opaque-run-page:") {
		t.Fatalf("fixture did not traverse opaque pagination: %+v", store.used)
	}
	page, err := f.service.SubagentActivities(f.ctx, parent.id, "", 100, true)
	if err != nil || len(page.Activities) != 66 {
		t.Fatalf("backfill lost a lifecycle fact: count=%d error=%v", len(page.Activities), err)
	}
	result, err := session.Prompt(f.ctx, core.Input{ID: "opaque-tail", Message: core.User("One later instruction.")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := result.Run.Wait(f.ctx); err != nil {
		t.Fatal(err)
	}
	project()
	project()
	page, err = f.service.SubagentActivities(f.ctx, parent.id, "", 100, true)
	if err != nil || len(page.Activities) != 68 {
		t.Fatalf("public tail cursor replay duplicated or lost facts: count=%d error=%v", len(page.Activities), err)
	}
}

func activityRows(t *testing.T, f *fixture, parent string, count int) []domain.SubagentActivity {
	t.Helper()
	var page domain.SubagentActivityPage
	eventually(t, f.ctx, func() bool {
		var err error
		page, err = f.service.SubagentActivities(f.ctx, parent, "", 100, true)
		return err == nil && len(page.Activities) >= count
	})
	return page.Activities
}

func TestSubagentActivityTracksReusedRunsAndKeepsCancellationAfterDeletion(t *testing.T) {
	var block atomic.Bool
	var entered sync.Once
	started := make(chan struct{})
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if block.Load() && strings.Contains(string(encode(request["messages"])), "ACTIVITY_CHILD") {
			entered.Do(func() { close(started) })
			<-ctx.Done()
			return nil
		}
		return textReply("Completed bounded work.")
	})
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "activity-child", "ACTIVITY_CHILD: complete bounded work.", false)
	subagentTestFinished(t, f, child.SessionID)
	activityRows(t, f, parent.id, 2)
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "activity-followup", AgentControlInput{AgentID: child.SessionID, Input: "Complete another bounded instruction."}); err != nil {
		t.Fatal(err)
	}
	rows := activityRows(t, f, parent.id, 4)
	ids, runs := map[string]bool{}, map[string]int{}
	for _, row := range rows {
		if ids[row.ID] || row.ChildSessionID != child.SessionID || row.Name != child.Name || row.Time.IsZero() {
			t.Fatalf("duplicate or incorrect lifecycle identity: %+v", row)
		}
		ids[row.ID] = true
		runs[row.RunID]++
	}
	if len(runs) != 2 {
		t.Fatalf("reused child did not retain two Runs: %+v", runs)
	}
	for _, count := range runs {
		if count != 2 {
			t.Fatal("each completed Run needs its own start and end")
		}
	}
	block.Store(true)
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "activity-blocked", AgentControlInput{AgentID: child.SessionID, Input: "Wait for explicit cancellation."}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	activityRows(t, f, parent.id, 5)
	// Deletion closes the Run, then erases the source. Its real cancellation
	// must already be safely retained in the surviving parent's journal.
	if _, err := f.service.controlSubagent(f.ctx, parent.id, "activity-delete", AgentControlInput{AgentID: child.SessionID, Signal: "delete"}); err != nil {
		t.Fatal(err)
	}
	rows = activityRows(t, f, parent.id, 6)
	if rows[0].Status != "cancelled" || rows[0].ChildSessionID != child.SessionID {
		t.Fatalf("deleted child lost its confirmed stop: %+v", rows[0])
	}
	var removed domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &removed); !domain.IsMissing(err) {
		t.Fatal("child was not deleted", err)
	}
	if err := f.store.Maintenance(f.ctx, subagentActivityCursorKind, child.SessionID, &subagentActivityCursor{}); !domain.IsMissing(err) {
		t.Fatal("deleted child retained its UI cursor", err)
	}
}

func TestSubagentActivityRecordsActualModelFailure(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Parent remains usable.") })
	parent := subagentTestSeed(t, f)
	subagentFailureInstall401(f)
	child := subagentTestSpawn(t, f, parent, "activity-failure", "Fail through the real offline API adapter.", false)
	failed := subagentFailureWait(t, f, child.SessionID)
	rows := activityRows(t, f, parent.id, 2)
	if rows[0].Status != "failed" || rows[1].Status != "started" || rows[0].RunID != failed.LastRun.ID {
		t.Fatalf("model failure lifecycle differs: %+v", rows)
	}
}

func TestSubagentActivityBackfillsWithoutReplayingDeliveredReports(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Report already delivered.") })
	parent := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, parent, "activity-upgrade", "Finish the existing historical work.", false)
	state := subagentTestFinished(t, f, child.SessionID)
	subagentWorkerCheckpoint(t, f, child.SessionID, state)
	f.idle(t, parent.id)
	// Simulate a pre-feature installation: ordinary delivery is caught up, but
	// no lifecycle projection exists yet. Its business checkpoint stays intact.
	if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error {
		if err := tx.PurgeSubagentActivities(f.ctx, parent.id); err != nil {
			return err
		}
		return tx.DeleteMaintenance(f.ctx, subagentActivityCursorKind, child.SessionID)
	}); err != nil {
		t.Fatal(err)
	}
	before := encode(parent.core.Load().State())
	requests := f.count()
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	rows := activityRows(t, f, parent.id, 2)
	if len(rows) != 2 || rows[0].Status != "completed" || rows[1].Status != "started" {
		t.Fatalf("caught-up business delivery skipped historical UI facts: %+v", rows)
	}
	if !bytes.Equal(before, encode(parent.core.Load().State())) || requests != f.count() {
		t.Fatal("backfill replayed reports or restarted the parent")
	}
}

func TestSubagentActivityProjectionIsIdempotentAndIndependentOfModelContext(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Seed.") })
	parent := subagentTestSeed(t, f)
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Enabled = new(bool)
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	before := encode(parent.core.Load().State())
	requests := f.count()
	child := domain.Session{Meta: domain.Meta{ID: "activity-a"}, AgentID: f.agent.ID, SubagentParentID: parent.id, Title: "Reader"}
	newer := core.Entry{ID: "run-newer", SessionID: child.ID, Sequence: 10, Version: 2, Kind: core.EntryRun, Time: time.Now().UTC(), Run: &core.RunRecord{ID: "a", Status: "started"}}
	if err := f.service.recordSubagentActivity(f.ctx, child, newer); err != nil {
		t.Fatal(err)
	}
	older := newer
	older.ID, older.SessionID, older.Time = "run-older", "activity-b", newer.Time.Add(-time.Hour)
	other := child
	other.ID = older.SessionID
	if err := f.service.recordSubagentActivity(f.ctx, other, older); err != nil {
		t.Fatal(err)
	}
	watch := f.store.Watch(persistence.ChangeKey{Kind: persistence.SubagentActivityKind, ID: parent.id})
	defer watch.Close()
	changed := watch.Changes()
	for range 3 {
		if err := f.service.recordSubagentActivity(f.ctx, child, newer); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-changed:
		t.Fatal("replay wrote another UI event")
	default:
	}
	page, err := f.service.SubagentActivities(f.ctx, parent.id, "1", 100, false)
	if err != nil || len(page.Activities) != 1 || page.Activities[0].ID != other.ID+":"+older.ID || page.Cursor != "2" {
		t.Fatalf("append cursor lost a late older fact: %+v %v", page, err)
	}
	if !bytes.Equal(before, encode(parent.core.Load().State())) || f.count() != requests {
		t.Fatal("UI facts changed model context or triggered generation")
	}
	if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error { return tx.PurgeSubagentActivities(f.ctx, parent.id) }); err != nil {
		t.Fatal(err)
	}
	page, err = f.service.SubagentActivities(f.ctx, parent.id, "", 100, true)
	if err != nil || len(page.Activities) != 0 {
		t.Fatal("parent cleanup retained its UI journal", err)
	}
}

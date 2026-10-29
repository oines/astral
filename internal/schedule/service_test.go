package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func fixture(t *testing.T, deliver func(context.Context, Occurrence) error) (*Service, *time.Time) {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		agent := domain.Agent{Meta: domain.Meta{ID: id}, Name: id}
		session := domain.Session{Meta: domain.Meta{ID: "session_" + id}, AgentID: id}
		if err := store.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(ctx, "sessions", &session.Meta, &session); err != nil {
			t.Fatal(err)
		}
	}
	setZone(t, store, "UTC")
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	s := New(store, deliver, nil, nil)
	s.now = func() time.Time { return now }
	return s, &now
}

func setZone(t *testing.T, store *persistence.Store, name string) {
	t.Helper()
	var preferences struct {
		domain.Meta
		domain.Preferences
	}
	if err := store.Get(context.Background(), "preferences", "owner", &preferences); err != nil && !domain.IsMissing(err) {
		t.Fatal(err)
	}
	preferences.ID, preferences.Timezone = "owner", name
	if err := store.Save(context.Background(), "preferences", &preferences.Meta, &preferences); err != nil {
		t.Fatal(err)
	}
}

func once(at time.Time) Input {
	return Input{SessionID: "session_a", Title: "Reminder", Prompt: "Check the project", Enabled: true, Timing: domain.ScheduleTiming{Kind: "once", At: &at}}
}

func mustCreate(t *testing.T, s *Service, input Input) domain.Schedule {
	t.Helper()
	plan, err := s.Create(context.Background(), "a", input)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestOnceDeliveryIsConsumedOnceAndBroadcastToAllSubscribers(t *testing.T) {
	var deliveries []Occurrence
	s, now := fixture(t, func(_ context.Context, occurrence Occurrence) error {
		deliveries = append(deliveries, occurrence)
		return nil
	})
	first, unsubscribe := s.Subscribe()
	defer unsubscribe()
	second, unsubscribeSecond := s.Subscribe()
	defer unsubscribeSecond()
	at := now.Add(time.Minute)
	plan := mustCreate(t, s, once(at))
	for _, subscriber := range []<-chan struct{}{first, second} {
		select {
		case <-subscriber:
		default:
			t.Fatal("schedule creation was not broadcast to every subscriber")
		}
	}
	if err := s.Step(context.Background(), at.Add(-time.Nanosecond)); err != nil || len(deliveries) != 0 {
		t.Fatalf("delivered before due time: %v, %+v", err, deliveries)
	}
	for range 2 {
		if err := s.Step(context.Background(), at); err != nil {
			t.Fatal(err)
		}
	}
	if len(deliveries) != 1 || !deliveries[0].ScheduledAt.Equal(at) || deliveries[0].ScheduleID != plan.ID {
		t.Fatalf("unexpected delivery: %+v", deliveries)
	}
	got, err := s.Get(context.Background(), "a", plan.ID)
	if err != nil || got.Enabled || got.NextRunAt != nil || got.LastStatus != "delivered" || !got.LastRunAt.Equal(at) {
		t.Fatalf("incorrect completed state: %+v, %v", got, err)
	}
	persisted, err := json.Marshal(deliveries[0])
	if err != nil {
		t.Fatal(err)
	}
	var replay Occurrence
	if err := json.Unmarshal(persisted, &replay); err != nil {
		t.Fatal(err)
	}
	if deliveries[0].InputID() != replay.InputID() {
		t.Fatal("occurrence identity changed across persistence")
	}
	other := deliveries[0]
	other.ScheduledAt = other.ScheduledAt.Add(time.Second)
	if other.InputID() == deliveries[0].InputID() {
		t.Fatal("different occurrences share an input identity")
	}
}

func TestRestartSkipsMissedOnceAndCronWithoutCatchup(t *testing.T) {
	count := 0
	s, now := fixture(t, func(context.Context, Occurrence) error { count++; return nil })
	oncePlan := mustCreate(t, s, once(now.Add(time.Minute)))
	setZone(t, s.store, "Asia/Singapore")
	cronPlan := mustCreate(t, s, Input{SessionID: "session_a", Title: "Daily", Prompt: "Daily check", Enabled: true, Timing: domain.ScheduleTiming{Kind: "cron", Cron: "0 8 * * *"}})
	expected := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	if !cronPlan.NextRunAt.Equal(expected) {
		t.Fatalf("timezone was not applied: %+v", cronPlan.NextRunAt)
	}
	*now = now.Add(72 * time.Hour)
	if err := s.recover(context.Background(), *now); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(context.Background(), *now); err != nil || count != 0 {
		t.Fatalf("restart caught up missed prompts: %d, %v", count, err)
	}
	gotOnce, _ := s.Get(context.Background(), "a", oncePlan.ID)
	gotCron, _ := s.Get(context.Background(), "a", cronPlan.ID)
	if gotOnce.Enabled || gotOnce.NextRunAt != nil || gotOnce.LastStatus != "missed" {
		t.Fatalf("missed one-time plan remains armed: %+v", gotOnce)
	}
	if !gotCron.Enabled || gotCron.NextRunAt == nil || !gotCron.NextRunAt.After(*now) || gotCron.LastStatus != "missed" {
		t.Fatalf("recurring plan did not advance to the future: %+v", gotCron)
	}
	if err := s.Step(context.Background(), *gotCron.NextRunAt); err != nil || count != 1 {
		t.Fatalf("future occurrence did not fire once: %d, %v", count, err)
	}
}

func TestEditsPauseDeleteAndBotScope(t *testing.T) {
	ctx := context.Background()
	count := 0
	s, now := fixture(t, func(context.Context, Occurrence) error { count++; return nil })
	input := once(now.Add(time.Minute))
	foreignTarget := input
	foreignTarget.SessionID = "session_b"
	if _, err := s.Create(ctx, "a", foreignTarget); !domain.IsMissing(err) {
		t.Fatalf("foreign Session was accepted: %v", err)
	}
	plan := mustCreate(t, s, input)
	if _, err := s.Get(ctx, "b", plan.ID); !domain.IsMissing(err) {
		t.Fatalf("foreign Bot could read plan: %v", err)
	}
	if _, err := s.Update(ctx, "b", plan.ID, input, plan.Revision); !domain.IsMissing(err) {
		t.Fatalf("foreign Bot could edit plan: %v", err)
	}
	if err := s.Delete(ctx, "b", plan.ID, plan.Revision); !domain.IsMissing(err) {
		t.Fatalf("foreign Bot could delete plan: %v", err)
	}
	if plans, err := s.List(ctx, "b"); err != nil || len(plans) != 0 {
		t.Fatalf("foreign plan leaked in list: %+v, %v", plans, err)
	}
	input.Enabled = false
	paused, err := s.Update(ctx, "a", plan.ID, input, plan.Revision)
	if err != nil || paused.NextRunAt != nil {
		t.Fatalf("pause failed: %+v, %v", paused, err)
	}
	if _, err := s.Update(ctx, "a", plan.ID, input, plan.Revision); err == nil {
		t.Fatal("stale revision overwrote an edit")
	}
	if err := s.Step(ctx, now.Add(time.Hour)); err != nil || count != 0 {
		t.Fatalf("paused plan fired: %d, %v", count, err)
	}
	input.Enabled, input.Title = true, "Edited title"
	resumed, err := s.Update(ctx, "a", plan.ID, input, paused.Revision)
	if err != nil || !resumed.NextRunAt.Equal(*input.Timing.At) {
		t.Fatalf("resume failed: %+v, %v", resumed, err)
	}
	input.Prompt = "Use this edited prompt"
	edited, err := s.Update(ctx, "a", plan.ID, input, resumed.Revision)
	if err != nil || !edited.NextRunAt.Equal(*resumed.NextRunAt) {
		t.Fatalf("prompt edit changed due time: %+v, %v", edited, err)
	}
	if err := s.Delete(ctx, "a", plan.ID, edited.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(ctx, now.Add(time.Hour)); err != nil || count != 0 {
		t.Fatalf("deleted plan fired: %d, %v", count, err)
	}
	retained, err := s.store.Retained(ctx, Kind)
	if err != nil || len(retained) != 0 {
		t.Fatalf("deleted plan retained prompt revisions: %+v, %v", retained, err)
	}
}

func TestInvalidTimesAndCron(t *testing.T) {
	s, now := fixture(t, nil)
	for _, timing := range []domain.ScheduleTiming{
		{Kind: "once", At: now},
		{Kind: "once"},
		{Kind: "once", At: ptr(now.Add(time.Hour)), Cron: "* * * * *"},
		{Kind: "cron", Cron: "@every 1m"},
		{Kind: "cron", Cron: "* * * * * *"},
		{Kind: "cron", Cron: "61 * * * *"},
		{Kind: "cron", Cron: "0 0 30 2 *"},
		{Kind: "cron", Cron: "CRON_TZ=Asia/Singapore 0 9 * * *"},
		{Kind: "unknown"},
	} {
		input := once(now.Add(time.Minute))
		input.Timing = timing
		if _, err := s.Create(context.Background(), "a", input); err == nil {
			t.Errorf("invalid timing was accepted: %+v", timing)
		}
	}
}

func ptr(at time.Time) *time.Time { return &at }

func TestInterruptedClaimIsInspectedAndNeverRedelivered(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "not accepted", true: "already accepted"}[accepted], func(t *testing.T) {
			count := 0
			ctx, cancel := context.WithCancel(context.Background())
			s, now := fixture(t, func(_ context.Context, _ Occurrence) error {
				count++
				cancel() // Simulate shutdown after the durable claim.
				return ctx.Err()
			})
			plan := mustCreate(t, s, once(now.Add(time.Minute)))
			if err := s.Step(ctx, *plan.NextRunAt); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation was lost: %v", err)
			}
			s.accepted = func(_ context.Context, occurrence Occurrence) (bool, error) {
				if occurrence.InputID() == "" || occurrence.Prompt != plan.Prompt {
					t.Fatalf("claim snapshot lost: %+v", occurrence)
				}
				return accepted, nil
			}
			if err := s.recover(context.Background(), now.Add(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := s.Step(context.Background(), now.Add(3*time.Minute)); err != nil || count != 1 {
				t.Fatalf("interrupted prompt was redelivered: %d, %v", count, err)
			}
			got, _ := s.Get(context.Background(), "a", plan.ID)
			want := "missed"
			if accepted {
				want = "delivered"
			}
			if got.LastStatus != want {
				t.Fatalf("receipt was not respected: %+v", got)
			}
		})
	}
}

func TestConcurrentTicksDoNotDuplicateAdmission(t *testing.T) {
	var mu sync.Mutex
	count := 0
	entered, release := make(chan struct{}), make(chan struct{})
	s, now := fixture(t, func(context.Context, Occurrence) error {
		mu.Lock()
		count++
		mu.Unlock()
		close(entered)
		<-release
		return nil
	})
	plan := mustCreate(t, s, once(now.Add(time.Minute)))
	results := make(chan error, 2)
	go func() { results <- s.Step(context.Background(), *plan.NextRunAt) }()
	<-entered
	go func() { results <- s.Step(context.Background(), *plan.NextRunAt) }()
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("duplicate admission: %d", count)
	}
}

func TestDeletedTargetDisarmsPlan(t *testing.T) {
	ctx := context.Background()
	s, now := fixture(t, func(context.Context, Occurrence) error { t.Fatal("deleted target was invoked"); return nil })
	plan := mustCreate(t, s, once(now.Add(time.Minute)))
	var session domain.Session
	if err := s.store.Get(ctx, "sessions", plan.SessionID, &session); err != nil {
		t.Fatal(err)
	}
	session.Deletion = &domain.SessionDeletion{}
	if err := s.store.Save(ctx, "sessions", &session.Meta, &session); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(ctx, *plan.NextRunAt); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "a", plan.ID)
	if got.Enabled || got.LastStatus != "failed" || got.LastError == "" {
		t.Fatalf("deleted target remained armed: %+v", got)
	}
	if err := s.store.Update(ctx, func(tx *persistence.Tx) error { return PurgeSession(ctx, tx, plan.SessionID) }); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.store.List(ctx, Kind)
	if len(rows) != 0 {
		raw, _ := json.Marshal(rows)
		t.Fatalf("Session cleanup retained a plan: %s", raw)
	}
}

func TestDeliveryFailureIsVisibleAndDoesNotRetryOccurrence(t *testing.T) {
	count := 0
	s, now := fixture(t, func(context.Context, Occurrence) error {
		count++
		return errors.New("Computer is offline")
	})
	plan := mustCreate(t, s, Input{SessionID: "session_a", Title: "Check", Prompt: "Check project", Enabled: true, Timing: domain.ScheduleTiming{Kind: "cron", Cron: "* * * * *"}})
	due := *plan.NextRunAt
	*now = due
	for range 2 {
		if err := s.Step(context.Background(), *now); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Get(context.Background(), "a", plan.ID)
	if count != 1 || got.LastStatus != "failed" || got.LastError != "Computer is offline" || !got.NextRunAt.After(due) {
		t.Fatalf("failed occurrence was retried or hidden: %d, %+v", count, got)
	}
}

func TestSlowTargetDoesNotBlockAnotherSchedule(t *testing.T) {
	entered, release, fast := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s, now := fixture(t, func(_ context.Context, occurrence Occurrence) error {
		if occurrence.Title == "Slow" {
			close(entered)
			<-release
		} else {
			close(fast)
		}
		return nil
	})
	input := once(now.Add(time.Minute))
	input.Title = "Slow"
	slow := mustCreate(t, s, input)
	input.Title = "Fast"
	mustCreate(t, s, input)
	done := make(chan error, 1)
	go func() { done <- s.Step(context.Background(), *slow.NextRunAt) }()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("slow plan never started")
	}
	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Fatal("slow target blocked another plan's admission")
	}
}

func TestLaterTickResolvesUncertainAcceptanceWithoutRetry(t *testing.T) {
	count := 0
	ctx, cancel := context.WithCancel(context.Background())
	s, now := fixture(t, func(context.Context, Occurrence) error {
		count++
		cancel()
		return context.Canceled
	})
	plan := mustCreate(t, s, once(now.Add(time.Minute)))
	if err := s.Step(ctx, *plan.NextRunAt); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.accepted = func(context.Context, Occurrence) (bool, error) { return true, nil }
	if err := s.Step(context.Background(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(context.Background(), "a", plan.ID)
	if count != 1 || got.LastStatus != "delivered" {
		t.Fatalf("uncertain admission was repeated or left stuck: %d, %+v", count, got)
	}
}

func TestStartSkipsDowntimeAndCloseStopsWorker(t *testing.T) {
	s, now := fixture(t, func(context.Context, Occurrence) error { t.Fatal("startup delivered a missed task"); return nil })
	plan := mustCreate(t, s, once(now.Add(time.Minute)))
	*now = now.Add(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx); err != nil {
		t.Fatalf("starting an already running scheduler was not idempotent: %v", err)
	}
	got, _ := s.Get(ctx, "a", plan.ID)
	if got.Enabled || got.LastStatus != "missed" {
		t.Fatalf("Start did not align downtime: %+v", got)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatalf("Close was not idempotent: %v", err)
	}
	if err := s.Start(ctx); err == nil {
		t.Fatal("closed scheduler restarted")
	}
}

func TestSessionModeValidationAndSwitching(t *testing.T) {
	s, now := fixture(t, nil)
	ctx := context.Background()
	input := once(now.Add(time.Hour))
	for _, invalid := range []Input{
		{SessionMode: "other", Title: input.Title, Prompt: input.Prompt, Timing: input.Timing},
		{SessionMode: "existing", Title: input.Title, Prompt: input.Prompt, Timing: input.Timing},
		{SessionMode: "new", SessionID: "session_a", Title: input.Title, Prompt: input.Prompt, Timing: input.Timing},
	} {
		if _, err := s.Create(ctx, "a", invalid); err == nil {
			t.Fatalf("invalid Session mode was accepted: %+v", invalid)
		}
	}
	plan := mustCreate(t, s, input)
	if plan.SessionMode != "existing" {
		t.Fatalf("omitted mode did not select existing: %+v", plan)
	}
	input.SessionMode, input.SessionID = "new", ""
	newMode, err := s.Update(ctx, "a", plan.ID, input, plan.Revision)
	if err != nil || newMode.SessionMode != "new" || newMode.SessionID != "" || newMode.LastSessionID != "" {
		t.Fatalf("switching to new mode failed: %+v, %v", newMode, err)
	}
	input.SessionMode, input.SessionID = "existing", "session_a"
	existing, err := s.Update(ctx, "a", plan.ID, input, newMode.Revision)
	if err != nil || existing.SessionMode != "existing" || existing.SessionID != "session_a" {
		t.Fatalf("switching back to existing mode failed: %+v, %v", existing, err)
	}
	rows, _ := s.store.List(ctx, "sessions")
	if len(rows) != 2 {
		t.Fatal("editing a plan created a Session before it was due")
	}
}

func TestMissedNewSessionPlanDoesNotCreateSession(t *testing.T) {
	s, now := fixture(t, func(context.Context, Occurrence) error {
		t.Fatal("missed new-Session occurrence invoked delivery")
		return nil
	})
	input := once(now.Add(time.Minute))
	input.SessionMode, input.SessionID = "new", ""
	plan := mustCreate(t, s, input)
	*now = now.Add(time.Hour)
	if err := s.recover(context.Background(), *now); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(context.Background(), *now); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(context.Background(), "a", plan.ID)
	rows, _ := s.store.List(context.Background(), "sessions")
	if got.LastStatus != "missed" || got.LastSessionID != "" || len(rows) != 2 {
		t.Fatalf("missed occurrence created a Session: %+v, %d Sessions", got, len(rows))
	}
}

func TestGlobalTimezoneChangeRecalculatesCronButPreservesOnceInstant(t *testing.T) {
	count := 0
	s, now := fixture(t, func(context.Context, Occurrence) error { count++; return nil })
	cronPlan := mustCreate(t, s, Input{SessionID: "session_a", Title: "Daily", Prompt: "Check", Enabled: true, Timing: domain.ScheduleTiming{Kind: "cron", Cron: "0 9 * * *"}})
	oneTime := now.Add(6 * time.Hour)
	oncePlan := mustCreate(t, s, once(oneTime))
	setZone(t, s.store, "Asia/Singapore")
	if err := s.Step(context.Background(), *now); err != nil {
		t.Fatal(err)
	}
	gotCron, _ := s.Get(context.Background(), "a", cronPlan.ID)
	gotOnce, _ := s.Get(context.Background(), "a", oncePlan.ID)
	want := time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC)
	if !gotCron.NextRunAt.Equal(want) || gotCron.LastStatus != "" || !gotOnce.NextRunAt.Equal(oneTime) || count != 0 {
		t.Fatalf("timezone change did not preserve plan semantics: cron=%+v once=%+v calls=%d", gotCron, gotOnce, count)
	}
}

func TestChangedTimezoneDoesNotClaimAnOldZoneDueOccurrence(t *testing.T) {
	count := 0
	s, now := fixture(t, func(context.Context, Occurrence) error { count++; return nil })
	plan := mustCreate(t, s, Input{SessionID: "session_a", Title: "Daily", Prompt: "Check", Enabled: true, Timing: domain.ScheduleTiming{Kind: "cron", Cron: "0 13 * * *"}})
	oldDue := *plan.NextRunAt
	setZone(t, s.store, "America/New_York")
	*now = oldDue
	if err := s.Step(context.Background(), *now); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(context.Background(), "a", plan.ID)
	want := time.Date(2026, 10, 29, 17, 0, 0, 0, time.UTC)
	if count != 0 || !got.NextRunAt.Equal(want) || got.LastRunAt != nil {
		t.Fatalf("old timezone caused an unintended run: %+v, calls=%d", got, count)
	}
	if err := s.Step(context.Background(), want); err != nil || count != 1 {
		t.Fatalf("new-zone future occurrence did not run: calls=%d err=%v", count, err)
	}
}

func TestStartupUsesChangedGlobalZoneAndCronKeepsDSTRules(t *testing.T) {
	s, now := fixture(t, nil)
	plan := mustCreate(t, s, Input{SessionID: "session_a", Title: "Daily", Prompt: "Check", Enabled: true, Timing: domain.ScheduleTiming{Kind: "cron", Cron: "0 13 * * *"}})
	setZone(t, s.store, "Asia/Singapore")
	if err := s.recover(context.Background(), *now); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(context.Background(), "a", plan.ID)
	want := time.Date(2026, 10, 30, 5, 0, 0, 0, time.UTC)
	if !got.NextRunAt.Equal(want) {
		t.Fatalf("startup retained an old-zone next time: %+v", got)
	}
	setZone(t, s.store, "America/New_York")
	*now = time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC)
	dst := mustCreate(t, s, Input{SessionID: "session_a", Title: "DST", Prompt: "Check", Enabled: true, Timing: domain.ScheduleTiming{Kind: "cron", Cron: "30 2 * * *"}})
	wantDST := time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC)
	if !dst.NextRunAt.Equal(wantDST) {
		t.Fatalf("cron treated the timezone as a fixed offset: %s, want %s", dst.NextRunAt, wantDST)
	}
}

package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func archiveTarget(t *testing.T, s *Service, id string) {
	t.Helper()
	ctx := context.Background()
	if err := s.store.Update(ctx, func(tx *persistence.Tx) error {
		var session domain.Session
		if err := tx.Get(ctx, "sessions", id, &session); err != nil {
			return err
		}
		session.Archived = true
		if err := tx.Save(ctx, "sessions", &session.Meta, &session); err != nil {
			return err
		}
		return PauseSession(ctx, tx, id)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestArchivePausesOnlyFixedTargetAndRestorationDoesNotResumePlans(t *testing.T) {
	ctx := context.Background()
	delivered := make(chan Occurrence, 8)
	s, now := fixture(t, func(_ context.Context, o Occurrence) error { delivered <- o; return nil })
	input := Input{SessionID: "session_a", Title: "Keep configuration", Prompt: "Review changes", Enabled: true, Timing: domain.ScheduleTiming{Kind: "cron", Cron: "* * * * *"}}
	plan := mustCreate(t, s, input)
	other := domain.Session{Meta: domain.Meta{ID: "session_other"}, AgentID: "a"}
	if err := s.store.Save(ctx, "sessions", &other.Meta, &other); err != nil {
		t.Fatal(err)
	}
	otherInput := input
	otherInput.SessionID = other.ID
	otherPlan := mustCreate(t, s, otherInput)
	newInput := input
	newInput.SessionMode, newInput.SessionID = "new", ""
	newPlan, err := s.Create(persistence.WithSessionScope(ctx, "a", "session_a"), "a", newInput)
	if err != nil {
		t.Fatal(err)
	}
	var stored record
	if err = s.store.Get(ctx, Kind, plan.ID, &stored); err != nil {
		t.Fatal(err)
	}
	stored.Pending = &Occurrence{ScheduleID: plan.ID, AgentID: "a", SessionID: "session_a", ScheduledAt: *stored.NextRunAt}
	stored.LastStatus = "dispatching"
	if err = s.store.Save(ctx, Kind, &stored.Meta, &stored); err != nil {
		t.Fatal(err)
	}
	archiveTarget(t, s, "session_a")
	stored = record{}
	if err = s.store.Get(ctx, Kind, plan.ID, &stored); err != nil || stored.Enabled || stored.NextRunAt != nil || stored.Pending != nil || stored.Title != input.Title || stored.Prompt != input.Prompt || stored.LastStatus != "missed" {
		t.Fatalf("archive did not retain a paused configuration: %+v %v", stored, err)
	}
	for _, id := range []string{otherPlan.ID, newPlan.ID} {
		got, err := s.Get(ctx, "a", id)
		if err != nil || !got.Enabled || got.NextRunAt == nil {
			t.Fatalf("archive affected another delivery target: %+v %v", got, err)
		}
	}
	items, err := s.List(ctx, "a")
	if err != nil || len(items) != 3 {
		t.Fatalf("archived target prevented management reads: %+v %v", items, err)
	}
	input.Enabled, input.Title = false, "Edited while archived"
	updated, err := s.Update(ctx, "a", plan.ID, input, stored.Revision)
	if err != nil {
		t.Fatal(err)
	}
	input.Enabled = true
	if _, err = s.Update(ctx, "a", plan.ID, input, updated.Revision); !archiveConflict(err) {
		t.Fatalf("enabled an archived target: %v", err)
	}
	input.Enabled = false
	if _, err = s.Create(ctx, "a", input); !archiveConflict(err) {
		t.Fatalf("created a plan for an archived target: %v", err)
	}
	var session domain.Session
	if err = s.store.Get(ctx, "sessions", "session_a", &session); err != nil {
		t.Fatal(err)
	}
	session.Archived = false
	if err = s.store.Save(ctx, "sessions", &session.Meta, &session); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(24 * time.Hour)
	if err = s.Step(ctx, *now); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 {
		t.Fatalf("restoration replayed a paused plan or stopped another target: %d", len(delivered))
	}
	for range 2 {
		if o := <-delivered; o.ScheduleID == plan.ID {
			t.Fatal("archived occurrence was replayed after restoration")
		}
	}
	got, err := s.Get(ctx, "a", plan.ID)
	if err != nil || got.Enabled || got.NextRunAt != nil {
		t.Fatalf("restoration enabled the plan: %+v %v", got, err)
	}
	input.Enabled = true
	resumed, err := s.Update(ctx, "a", plan.ID, input, got.Revision)
	if err != nil || resumed.NextRunAt == nil || !resumed.NextRunAt.After(*now) {
		t.Fatalf("explicit enable did not start from the present: %+v %v", resumed, err)
	}
	archiveTarget(t, s, "session_a")
	got, err = s.Get(ctx, "a", plan.ID)
	if err != nil || s.Delete(ctx, "a", plan.ID, got.Revision) != nil {
		t.Fatalf("archived target prevented plan deletion: %v", err)
	}
}

func TestArchiveInvalidatesAClaimWhileItsDeliveryCallbackReturns(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	s, now := fixture(t, func(context.Context, Occurrence) error { close(entered); <-release; return nil })
	at := now.Add(time.Minute)
	plan := mustCreate(t, s, once(at))
	go func() { done <- s.Step(context.Background(), at) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("delivery callback did not start")
	}
	archiveTarget(t, s, "session_a")
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("returning callback deadlocked against archive")
	}
	var stored record
	if err := s.store.Get(context.Background(), Kind, plan.ID, &stored); err != nil || stored.Pending != nil || stored.Enabled || stored.NextRunAt != nil || stored.LastStatus != "missed" {
		t.Fatalf("stale callback rewrote the archived claim: %+v %v", stored, err)
	}
}

func TestLegacyArchivedTargetCannotDispatchAnEnabledPlan(t *testing.T) {
	calls := 0
	s, now := fixture(t, func(context.Context, Occurrence) error { calls++; return nil })
	at := now.Add(time.Minute)
	plan := mustCreate(t, s, once(at))
	var session domain.Session
	if err := s.store.Get(context.Background(), "sessions", "session_a", &session); err != nil {
		t.Fatal(err)
	}
	session.Archived = true // Simulate a record archived by an older Host.
	if err := s.store.Save(context.Background(), "sessions", &session.Meta, &session); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(context.Background(), at); err != nil || calls != 0 {
		t.Fatalf("archived target was dispatched: calls=%d error=%v", calls, err)
	}
	got, err := s.Get(context.Background(), "a", plan.ID)
	if err != nil || got.Enabled || got.NextRunAt != nil {
		t.Fatalf("legacy plan was not paused: %+v %v", got, err)
	}
}

func archiveConflict(err error) bool {
	var problem *domain.Error
	return errors.As(err, &problem) && problem.Status == 409
}

func TestPauseSessionsKeepsOtherTargetsAndNewSessionPlans(t *testing.T) {
	s, _ := fixture(t, nil)
	ctx := context.Background()
	plans := []domain.Schedule{}
	for _, id := range []string{"session_a", "second", "unrelated"} {
		if id != "session_a" {
			session := domain.Session{Meta: domain.Meta{ID: id}, AgentID: "a"}
			if err := s.store.Save(ctx, "sessions", &session.Meta, &session); err != nil {
				t.Fatal(err)
			}
		}
		plans = append(plans, mustCreate(t, s, Input{SessionID: id, Title: id, Prompt: "Keep this configuration", Enabled: true, Timing: domain.ScheduleTiming{Kind: "cron", Cron: "* * * * *"}}))
	}
	newPlan, err := s.Create(persistence.WithSessionScope(ctx, "a", "session_a"), "a", Input{SessionMode: "new", Title: "New target per run", Prompt: "Keep creating independent Sessions", Enabled: true, Timing: domain.ScheduleTiming{Kind: "cron", Cron: "* * * * *"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		return PauseSessions(ctx, tx, []string{"session_a", "second", "session_a"})
	}); err != nil {
		t.Fatal(err)
	}
	for i, plan := range append(plans, newPlan) {
		got, err := s.Get(ctx, "a", plan.ID)
		wantEnabled := i >= 2
		if err != nil || got.Enabled != wantEnabled || (got.NextRunAt != nil) != wantEnabled {
			t.Fatalf("bulk pause affected wrong target: index=%d plan=%+v error=%v", i, got, err)
		}
	}
}

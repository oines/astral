package schedule

import (
	"context"
	"errors"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestSchedulesCannotDriveSubagentsOrBeControlledByThem(t *testing.T) {
	s, now := fixture(t, nil)
	ctx := context.Background()
	child := domain.Session{Meta: domain.Meta{ID: "child"}, AgentID: "a", SubagentParentID: "session_a"}
	if err := s.store.Save(ctx, "sessions", &child.Meta, &child); err != nil {
		t.Fatal(err)
	}
	input := once(now.AddDate(0, 0, 1))
	plan := mustCreate(t, s, input)
	childTarget := input
	childTarget.SessionID = child.ID
	for name, caller := range map[string]context.Context{
		"Owner":         ctx,
		"direct parent": persistence.WithSessionScope(ctx, "a", "session_a"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Create(caller, "a", childTarget); !errors.Is(err, errSubagentSchedule) {
				t.Fatalf("schedule creation targeted child: %v", err)
			}
			if _, err := s.Update(caller, "a", plan.ID, childTarget, plan.Revision); !errors.Is(err, errSubagentSchedule) {
				t.Fatalf("schedule retargeted to child: %v", err)
			}
		})
	}
	caller := persistence.WithSessionScope(ctx, "a", child.ID)
	if _, err := s.Get(caller, "a", plan.ID); err != nil {
		t.Fatalf("child lost ordinary schedule read access: %v", err)
	}
	if plans, err := s.List(caller, "a"); err != nil || len(plans) != 1 {
		t.Fatalf("child schedule catalog: %+v %v", plans, err)
	}
	if _, err := s.Create(caller, "a", input); !errors.Is(err, errSubagentSchedule) {
		t.Fatalf("child used schedule to control its parent: %v", err)
	}
	newSession := input
	newSession.SessionMode, newSession.SessionID = "new", ""
	if _, err := s.Create(caller, "a", newSession); !errors.Is(err, errSubagentSchedule) {
		t.Fatalf("child used schedule to create an uncontrolled Session: %v", err)
	}
	if _, err := s.Update(caller, "a", plan.ID, input, plan.Revision); !errors.Is(err, errSubagentSchedule) {
		t.Fatalf("child mutated an ordinary schedule: %v", err)
	}
	if err := s.Delete(caller, "a", plan.ID, plan.Revision); !errors.Is(err, errSubagentSchedule) {
		t.Fatalf("child deleted an ordinary schedule: %v", err)
	}
	unchanged, err := s.Get(ctx, "a", plan.ID)
	if err != nil || unchanged.SessionID != input.SessionID || unchanged.Revision != plan.Revision {
		t.Fatalf("rejected schedule writes changed state: %+v %v", unchanged, err)
	}
}

func TestScheduleExecutionRechecksSubagentTargetAndSource(t *testing.T) {
	for _, mode := range []string{"existing", "new"} {
		t.Run(mode, func(t *testing.T) {
			deliveries := 0
			s, now := fixture(t, func(context.Context, Occurrence) error { deliveries++; return nil })
			ctx := context.Background()
			child := domain.Session{Meta: domain.Meta{ID: "child"}, AgentID: "a", SubagentParentID: "session_a"}
			if err := s.store.Save(ctx, "sessions", &child.Meta, &child); err != nil {
				t.Fatal(err)
			}
			at := now.AddDate(0, 0, 1)
			input := once(at)
			if mode == "new" {
				input.SessionMode, input.SessionID = mode, ""
			}
			plan := mustCreate(t, s, input)
			var persisted record
			if err := s.store.Get(ctx, Kind, plan.ID, &persisted); err != nil {
				t.Fatal(err)
			}
			// Simulate a stale/imported plan whose identity no longer satisfies
			// admission rules. Execution must not trust creation-time checks.
			if mode == "existing" {
				persisted.SessionID = child.ID
			} else {
				persisted.SourceSessionID = child.ID
			}
			if err := s.store.Save(ctx, Kind, &persisted.Meta, &persisted); err != nil {
				t.Fatal(err)
			}
			if err := s.Step(ctx, at); err != nil {
				t.Fatal(err)
			}
			read, err := s.Get(ctx, "a", plan.ID)
			if err != nil || deliveries != 0 || read.Enabled || read.NextRunAt != nil || read.LastStatus != "failed" {
				t.Fatalf("stale subagent plan executed: deliveries=%d plan=%+v error=%v", deliveries, read, err)
			}
		})
	}
}

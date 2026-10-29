package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/schedule"
)

func TestScheduleUsesSteerDuringActiveRunAndKeepsItsMessageSource(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			select {
			case <-ctx.Done():
				return nil
			case <-release:
			}
		}
		return textReply("Done")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "owner", Text: "Initial task"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	occurrence := schedule.Occurrence{ScheduleID: "schedule_test", AgentID: f.agent.ID, SessionID: f.session.ID, Title: "Scheduled check", Prompt: "Inspect project status", ScheduledAt: time.Now().UTC()}
	if err := f.service.deliverSchedule(f.ctx, occurrence); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.coreStore.LookupInput(f.ctx, f.session.ID, occurrence.InputID())
	if err != nil || receipt.Kind != core.InputSteer || receipt.Message == nil {
		t.Fatalf("scheduled input was not steered: %+v, %v", receipt, err)
	}
	var metadata messages.Metadata
	if err := json.Unmarshal(receipt.Message.Metadata, &metadata); err != nil || metadata.Source != "schedule" || metadata.Schedule == nil || metadata.Schedule.Title != occurrence.Title {
		t.Fatalf("schedule bubble metadata is incorrect: %+v, %v", metadata, err)
	}
	if err := f.service.deliverSchedule(f.ctx, occurrence); err != nil {
		t.Fatalf("same occurrence was not idempotent: %v", err)
	}
	close(release)
	f.idle(t, f.session.ID)
	f.mu.Lock()
	requests, _ := json.Marshal(f.requests)
	f.mu.Unlock()
	if !strings.Contains(string(requests), "Inspect project status") {
		t.Fatalf("running Agent did not receive the scheduled prompt: %s", requests)
	}
}

func TestPermanentSessionDeletionPurgesSchedule(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Done") })
	at := time.Now().Add(time.Hour)
	plan, err := f.service.Schedules.Create(f.ctx, f.agent.ID, schedule.Input{SessionID: f.session.ID, Title: "Later", Prompt: "Check later", Enabled: true, Timing: domain.ScheduleTiming{Kind: "once", At: &at}})
	if err != nil {
		t.Fatal(err)
	}
	record := archiveForDeletion(t, f, f.session.ID)
	if err := f.service.DeleteSession(f.ctx, record.ID, record.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Schedules.Get(f.ctx, f.agent.ID, plan.ID); err == nil {
		t.Fatal("permanently deleted Session retained its Schedule")
	}
}

func TestPermanentBotDeletionPurgesOnlyOwnedSchedules(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Done") })
	at := time.Now().Add(time.Hour)
	input := schedule.Input{SessionID: f.session.ID, Title: "Owned task", Prompt: "Check later", Enabled: true, Timing: domain.ScheduleTiming{Kind: "once", At: &at}}
	if _, err := f.service.Schedules.Create(f.ctx, f.agent.ID, input); err != nil {
		t.Fatal(err)
	}
	other := domain.Agent{Name: "Other Bot", DefaultModelID: f.agent.DefaultModelID}
	if err := f.service.SaveAgent(f.ctx, &other); err != nil {
		t.Fatal(err)
	}
	session, err := f.service.CreateSession(f.ctx, other.ID, "Other Session")
	if err != nil {
		t.Fatal(err)
	}
	input.SessionID = session.ID
	kept, err := f.service.Schedules.Create(f.ctx, other.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err != nil {
		t.Fatal(err)
	}
	plans, err := f.service.Schedules.List(f.ctx, "")
	if err != nil || len(plans) != 1 || plans[0].ID != kept.ID {
		t.Fatalf("Bot deletion did not preserve another Bot's plan: %+v, %v", plans, err)
	}
}

func TestNewSessionScheduleCreatesFreshSessionsAndUsesCurrentBotDefaults(t *testing.T) {
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any { return textReply("Done") })
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "original", Text: "must-not-be-copied"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	input := schedule.Input{SessionMode: "new", Title: "Daily review", Prompt: "Review only this fresh task", Enabled: true, Timing: domain.ScheduleTiming{Kind: "cron", Cron: "* * * * *"}}
	plan, err := f.service.Schedules.Create(f.ctx, f.agent.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.LastSessionID != "" || plan.SessionID != "" {
		t.Fatalf("plan exposed a Session before execution: %+v", plan)
	}
	due := *plan.NextRunAt
	if err := f.service.Schedules.Step(f.ctx, due); err != nil {
		t.Fatal(err)
	}
	firstRun, _ := f.service.Schedules.Get(f.ctx, f.agent.ID, plan.ID)
	if firstRun.LastStatus != "delivered" || firstRun.LastSessionID == "" {
		t.Fatalf("new Session was not delivered: %+v", firstRun)
	}
	firstView := f.idle(t, firstRun.LastSessionID)
	var first domain.Session
	if err := f.store.Get(f.ctx, "sessions", firstRun.LastSessionID, &first); err != nil {
		t.Fatal(err)
	}
	if first.AgentID != f.agent.ID || first.Source != "schedule" || first.ParentSessionID != "" || first.BindingID != "" || first.ConversationID != "" || first.ComputerID != f.agent.DefaultComputerID || firstView.ProfileID != f.agent.DefaultModelID {
		t.Fatalf("new Session did not use clean Bot defaults: %+v, %+v", first, firstView)
	}
	f.mu.Lock()
	firstWire := append([]byte(nil), f.requests[len(f.requests)-1]...)
	f.mu.Unlock()
	if strings.Contains(string(firstWire), "must-not-be-copied") {
		t.Fatalf("scheduled Session copied another Session's history: %s", firstWire)
	}

	occurrence := schedule.Occurrence{ScheduleID: plan.ID, AgentID: f.agent.ID, SessionMode: "new", SessionID: first.ID, Title: plan.Title, Prompt: plan.Prompt, ScheduledAt: due}
	before := f.count()
	f.restart(t)
	if err := f.service.deliverSchedule(f.ctx, occurrence); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.store.List(f.ctx, "sessions")
	if len(rows) != 2 || f.count() != before {
		t.Fatalf("same occurrence after restart created a second Session or run: %d Sessions, %d requests", len(rows), f.count())
	}

	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Meta, profile.Name = domain.Meta{}, "New default"
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	f.agent.DefaultModelID, f.agent.DefaultComputerID = profile.ID, ""
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Schedules.Step(f.ctx, *firstRun.NextRunAt); err != nil {
		t.Fatal(err)
	}
	secondRun, _ := f.service.Schedules.Get(f.ctx, f.agent.ID, plan.ID)
	if secondRun.LastSessionID == "" || secondRun.LastSessionID == first.ID {
		t.Fatalf("next occurrence reused the earlier Session: %+v", secondRun)
	}
	secondView := f.idle(t, secondRun.LastSessionID)
	var second domain.Session
	if err := f.store.Get(f.ctx, "sessions", secondRun.LastSessionID, &second); err != nil {
		t.Fatal(err)
	}
	if second.ComputerID != "" || secondView.ProfileID != profile.ID {
		t.Fatalf("new occurrence reused stale Bot defaults: %+v, %+v", second, secondView)
	}
	archived := archiveForDeletion(t, f, first.ID)
	if err := f.service.DeleteSession(f.ctx, first.ID, archived.Revision); err != nil {
		t.Fatal(err)
	}
	if kept, err := f.service.Schedules.Get(f.ctx, f.agent.ID, plan.ID); err != nil || !kept.Enabled {
		t.Fatalf("deleting a generated Session deleted its repeating plan: %+v, %v", kept, err)
	}
	if err := f.service.DeleteAgent(f.ctx, f.agent.ID, f.agent.Revision); err != nil {
		t.Fatal(err)
	}
	if tasks, err := f.service.Schedules.List(f.ctx, ""); err != nil || len(tasks) != 0 {
		t.Fatalf("Bot deletion kept its new-Session plans: %+v, %v", tasks, err)
	}
}

func TestFailedNewSessionCreationDoesNotExposePhantomSession(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Done") })
	f.agent.DefaultModelID = ""
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(time.Hour)
	plan, err := f.service.Schedules.Create(f.ctx, f.agent.ID, schedule.Input{SessionMode: "new", Title: "Later", Prompt: "Check", Enabled: true, Timing: domain.ScheduleTiming{Kind: "once", At: &at}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Schedules.Step(f.ctx, at); err != nil {
		t.Fatal(err)
	}
	got, _ := f.service.Schedules.Get(f.ctx, f.agent.ID, plan.ID)
	rows, _ := f.store.List(f.ctx, "sessions")
	if got.LastStatus != "failed" || got.LastSessionID != "" || len(rows) != 1 {
		t.Fatalf("failed creation exposed a phantom Session: %+v, %d Sessions", got, len(rows))
	}
}

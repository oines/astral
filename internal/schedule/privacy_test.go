package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestPrivateTargetPlansReauthorizeEveryOperation(t *testing.T) {
	s, now := fixture(t, nil)
	ctx := context.Background()
	reader := domain.Session{Meta: domain.Meta{ID: "reader"}, AgentID: "a"}
	if err := s.store.Save(ctx, "sessions", &reader.Meta, &reader); err != nil {
		t.Fatal(err)
	}
	input := once(now.Add(time.Hour))
	plan := mustCreate(t, s, input)
	other := persistence.WithSessionScope(ctx, "a", "reader")
	if _, err := s.Get(other, "a", plan.ID); err != nil {
		t.Fatal(err)
	}
	var target domain.Session
	if err := s.store.Get(ctx, "sessions", "session_a", &target); err != nil {
		t.Fatal(err)
	}
	target.Private = true
	if err := s.store.Save(ctx, "sessions", &target.Meta, &target); err != nil {
		t.Fatal(err)
	}
	if listed, err := s.List(other, "a"); err != nil || len(listed) != 0 {
		t.Fatalf("private target leaked through list: %+v %v", listed, err)
	}
	for name, action := range map[string]func() error{
		"get":    func() error { _, err := s.Get(other, "a", plan.ID); return err },
		"create": func() error { _, err := s.Create(other, "a", input); return err },
		"update": func() error { _, err := s.Update(other, "a", plan.ID, input, plan.Revision); return err },
		"delete": func() error { return s.Delete(other, "a", plan.ID, plan.Revision) },
	} {
		if err := action(); !domain.IsMissing(err) {
			t.Fatalf("%s allowed private target: %v", name, err)
		}
	}
	public := once(now.Add(time.Hour))
	public.SessionID = reader.ID
	publicPlan := mustCreate(t, s, public)
	if _, err := s.Update(other, "a", publicPlan.ID, input, publicPlan.Revision); !domain.IsMissing(err) {
		t.Fatalf("public plan could be retargeted to a hidden Session: %v", err)
	}
	own := persistence.WithSessionScope(ctx, "a", target.ID)
	if _, err := s.Get(own, "a", plan.ID); err != nil {
		t.Fatalf("private Session cannot manage its own plan: %v", err)
	}
	if listed, err := s.List(ctx, ""); err != nil || len(listed) != 2 {
		t.Fatalf("Owner lost schedule visibility: %+v %v", listed, err)
	}
}

func TestNewSessionPlansKeepGatewaySourcePrivacy(t *testing.T) {
	var delivered []Occurrence
	var s *Service
	var now *time.Time
	s, now = fixture(t, func(ctx context.Context, o Occurrence) error {
		delivered = append(delivered, o)
		child := domain.Session{Meta: domain.Meta{ID: o.SessionID}, AgentID: o.AgentID, Private: o.Private}
		return s.store.Save(ctx, "sessions", &child.Meta, &child)
	})
	ctx := context.Background()
	input := once(now.Add(time.Minute))
	input.SessionMode, input.SessionID = "new", ""
	source := persistence.WithSessionScope(ctx, "a", "session_a")
	plan, err := s.Create(source, "a", input)
	if err != nil || plan.SourceSessionID != "session_a" {
		t.Fatalf("command source was not captured: %+v %v", plan, err)
	}
	ownerPlan, err := s.Create(ctx, "a", input)
	if err != nil || ownerPlan.SourceSessionID != "" {
		t.Fatalf("Owner plan inherited a command source: %+v %v", ownerPlan, err)
	}
	var origin domain.Session
	if err := s.store.Get(ctx, "sessions", "session_a", &origin); err != nil {
		t.Fatal(err)
	}
	origin.Private = true
	if err := s.store.Save(ctx, "sessions", &origin.Meta, &origin); err != nil {
		t.Fatal(err)
	}
	other := persistence.WithSessionScope(ctx, "a", "another-session")
	if _, err := s.Get(other, "a", plan.ID); !domain.IsMissing(err) {
		t.Fatalf("new-Session plan exposed a private source prompt: %v", err)
	}
	if err := s.Step(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, o := range delivered {
		if o.ScheduleID == plan.ID && !o.Private {
			t.Fatal("new Session did not inherit the source's current privacy")
		}
		if o.ScheduleID == ownerPlan.ID && o.Private {
			t.Fatal("Owner-created schedule unexpectedly created a private Session")
		}
	}
	if len(delivered) != 2 {
		t.Fatalf("expected two scheduled deliveries, got %d", len(delivered))
	}
	read, err := s.Get(source, "a", plan.ID)
	if err != nil || read.LastSessionID == "" {
		t.Fatalf("private source lost its own plan after producing a private child: %+v %v", read, err)
	}
	var child domain.Session
	if err := s.store.Get(ctx, "sessions", read.LastSessionID, &child); err != nil {
		t.Fatal(err)
	}
	if err := persistence.CheckSessionAccess(source, child); !domain.IsMissing(err) {
		t.Fatalf("managing the source plan granted private child history access: %v", err)
	}
	if _, err := s.Update(source, "a", read.ID, input, read.Revision); err != nil {
		t.Fatalf("private source cannot edit its own plan after delivery: %v", err)
	}
	read, err = s.Get(source, "a", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(source, "a", read.ID, read.Revision); err != nil {
		t.Fatalf("private source cannot delete its own plan after delivery: %v", err)
	}
}

package channels

import (
	"context"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/testkit"
)

func TestNewIMSessionsInheritAgentReasoningWithoutChangingExistingSessions(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unexpected") })
	if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	p.Profile.Recommendations["reasoning"] = testkit.JSON(map[string]any{"efforts": []string{"low", "high"}})
	if err := p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
		t.Fatal(err)
	}
	p.Agent.DefaultReasoning = &core.ReasoningOptions{Effort: "high"}
	if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
		t.Fatal(err)
	}
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	first := createBinding(t, p, s, connection, "-100", "")
	p.Agent.DefaultReasoning = &core.ReasoningOptions{Effort: "low"}
	if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
		t.Fatal(err)
	}
	second := createBinding(t, p, s, connection, "-200", "")
	for _, expected := range []struct{ id, effort string }{{first.SessionID, "high"}, {second.SessionID, "low"}} {
		selection, err := p.Harness.ModelSelection(p.Context, expected.id)
		if err != nil {
			t.Fatal(err)
		}
		if selection.Reasoning == nil || selection.Reasoning.Effort != expected.effort {
			t.Fatalf("incorrect IM Session default: %+v", selection)
		}
	}
}

func TestProbeReasoningFollowsSessionOrExplicitProbeSelection(t *testing.T) {
	requests := make(chan map[string]any, 4)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		requests <- request
		return testkit.Tool("decide", map[string]bool{"participate": false})
	})
	if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	p.Profile.Recommendations["reasoning"] = testkit.JSON(map[string]any{"efforts": []string{"low", "high"}})
	if err := p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
		t.Fatal(err)
	}
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, connection, "-100", "")
	if err := p.Harness.SelectModel(p.Context, binding.SessionID, p.Profile.ID, &core.ReasoningOptions{Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	check := func(expected any) {
		t.Helper()
		if _, err := s.shouldParticipate(p.Context, binding, core.Context{}); err != nil {
			t.Fatal(err)
		}
		if request := <-requests; request["reasoning_effort"] != expected {
			t.Fatalf("Probe reasoning = %v, want %v", request["reasoning_effort"], expected)
		}
	}
	check("high")
	p.Agent.ProbeModelID, p.Agent.ProbeReasoning = p.Profile.ID, &core.ReasoningOptions{Effort: "low"}
	if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
		t.Fatal(err)
	}
	check("low")
	p.Agent.ProbeReasoning = nil
	if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
		t.Fatal(err)
	}
	check(nil)
	p.Agent.ProbeModelID = ""
	if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
		t.Fatal(err)
	}
	check("high")
}

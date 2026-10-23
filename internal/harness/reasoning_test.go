package harness

import (
	"context"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestAgentReasoningDefaultsAreCopiedIntoNewSessions(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Recommendations["reasoning"] = encode(map[string]any{"efforts": []string{"low", "high"}})
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	f.agent.DefaultReasoning = &core.ReasoningOptions{Effort: "high"}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	first, err := f.service.CreateSession(f.ctx, f.agent.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	if first.Reasoning == nil || first.Reasoning.Effort != "high" || first.Options.Reasoning.Effort != "high" {
		t.Fatalf("new Session did not inherit reasoning: %+v", first)
	}
	selected, err := f.service.Core(f.ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Configuration().Options.Reasoning.Effort != "high" {
		t.Fatal("Core did not use inherited reasoning")
	}
	f.agent.DefaultReasoning = &core.ReasoningOptions{Effort: "low"}
	if err = f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	second, err := f.service.CreateSession(f.ctx, f.agent.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	if second.Reasoning == nil || second.Reasoning.Effort != "low" {
		t.Fatal("later Session did not use updated default")
	}
	if err = f.store.Get(f.ctx, "sessions", first.ID, &first); err != nil {
		t.Fatal(err)
	}
	if first.Reasoning.Effort != "high" {
		t.Fatal("changing Bot defaults rewrote an existing Session")
	}
	if err = f.service.SelectModel(f.ctx, first.ID, profile.ID, nil); err != nil {
		t.Fatal(err)
	}
	first = domain.Session{Meta: domain.Meta{ID: first.ID}}
	if err = f.store.Get(f.ctx, "sessions", first.ID, &first); err != nil {
		t.Fatal(err)
	}
	if first.Reasoning != nil || first.Options.Reasoning.Effort != "" {
		t.Fatal("returning to model defaults retained the Bot override")
	}
	var agent domain.Agent
	if err = f.store.Get(f.ctx, "agents", f.agent.ID, &agent); err != nil {
		t.Fatal(err)
	}
	if agent.DefaultReasoning.Effort != "low" {
		t.Fatal("Session selection wrote back to Bot defaults")
	}
	f.restart(t)
	selected, err = f.service.Core(f.ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Configuration().Options.Reasoning.Effort != "low" {
		t.Fatal("inherited reasoning was lost after restart")
	}
}

func TestAgentReasoningRejectsMissingModelAndUnsupportedEffort(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("unexpected") })
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Recommendations["reasoning"] = encode(map[string]any{"efforts": []string{"low", "high"}})
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []bool{false, true} {
		for _, model := range []string{"", profile.ID} {
			agent := f.agent
			if probe {
				agent.ProbeModelID, agent.ProbeReasoning = model, &core.ReasoningOptions{Effort: "unsupported"}
			} else {
				agent.DefaultModelID, agent.DefaultReasoning = model, &core.ReasoningOptions{Effort: "unsupported"}
			}
			if err := f.service.SaveAgent(f.ctx, &agent); err == nil {
				t.Fatalf("accepted invalid reasoning (probe=%v model=%s)", probe, model)
			}
		}
	}
	if f.count() != 0 {
		t.Fatal("saving defaults sent a provider request")
	}
}

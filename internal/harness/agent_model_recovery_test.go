package harness

import (
	"context"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func agentModelRecoveryFixture(t *testing.T) (*fixture, domain.ModelProfile, domain.Provider, domain.ModelProfile) {
	t.Helper()
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("saving Bot settings dispatched a model request")
		return nil
	})
	var original domain.ModelProfile
	var provider domain.Provider
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &original); err != nil {
		t.Fatal(err)
	}
	original.Recommendations["reasoning"] = encode(map[string]any{"efforts": []string{"low", "high"}})
	if err := f.service.Models.SaveProfile(f.ctx, &original); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Get(f.ctx, "providers", original.ProviderID, &provider); err != nil {
		t.Fatal(err)
	}
	f.agent.ProbeModelID = original.ID
	f.agent.DefaultReasoning = &core.ReasoningOptions{Effort: "high"}
	f.agent.ProbeReasoning = &core.ReasoningOptions{Effort: "low"}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	replacementProvider := provider
	replacementProvider.Meta = domain.Meta{}
	if err := f.service.Models.SaveProvider(f.ctx, &replacementProvider, nil, nil); err != nil {
		t.Fatal(err)
	}
	replacement := original
	replacement.Meta, replacement.ProviderID = domain.Meta{}, replacementProvider.ID
	if err := f.service.Models.SaveProfile(f.ctx, &replacement); err != nil {
		t.Fatal(err)
	}
	return f, original, provider, replacement
}

func TestSaveAgentRepairsUnavailableModelSelectionsIndependently(t *testing.T) {
	for _, unavailable := range []string{"deleted-provider", "disabled-provider", "deleted-model", "disabled-model"} {
		for _, first := range []string{"default", "probe"} {
			t.Run(unavailable+"/"+first+"-first", func(t *testing.T) {
				f, original, provider, replacement := agentModelRecoveryFixture(t)
				var err error
				no := false
				switch unavailable {
				case "deleted-provider":
					err = f.service.Models.DeleteProvider(f.ctx, provider.ID, provider.Revision)
				case "disabled-provider":
					provider.Enabled = &no
					err = f.service.Models.SaveProvider(f.ctx, &provider, nil, nil)
				case "deleted-model":
					err = f.service.Models.DeleteProfile(f.ctx, original.ID, original.Revision)
				case "disabled-model":
					original.Enabled = &no
					err = f.service.Models.SaveProfile(f.ctx, &original)
				}
				if err != nil {
					t.Fatal(err)
				}
				before := f.agent
				f.agent.Name = "Renamed while both model selections are unavailable"
				if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
					t.Fatalf("unavailable model references blocked an unrelated edit: %v", err)
				}
				if f.agent.DefaultModelID != original.ID || f.agent.ProbeModelID != original.ID || !jsonEqual(f.agent.DefaultReasoning, before.DefaultReasoning) || !jsonEqual(f.agent.ProbeReasoning, before.ProbeReasoning) {
					t.Fatal("unrelated edit changed unavailable model selections")
				}
				if first == "default" {
					f.agent.DefaultModelID, f.agent.DefaultReasoning = replacement.ID, nil
				} else {
					f.agent.ProbeModelID, f.agent.ProbeReasoning = replacement.ID, nil
				}
				if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
					t.Fatalf("the other unavailable reference blocked the first repair: %v", err)
				}
				var saved domain.Agent
				if err := f.store.Get(f.ctx, "agents", f.agent.ID, &saved); err != nil {
					t.Fatal(err)
				}
				if first == "default" && (saved.ProbeModelID != original.ID || !jsonEqual(saved.ProbeReasoning, before.ProbeReasoning)) || first == "probe" && (saved.DefaultModelID != original.ID || !jsonEqual(saved.DefaultReasoning, before.DefaultReasoning)) {
					t.Fatal("repair implicitly changed the other unavailable reference")
				}
				f.agent.DefaultModelID, f.agent.DefaultReasoning = replacement.ID, nil
				f.agent.ProbeModelID, f.agent.ProbeReasoning = replacement.ID, nil
				if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
					t.Fatalf("could not repair the remaining reference: %v", err)
				}
				if err := f.store.Get(f.ctx, "agents", f.agent.ID, &saved); err != nil || saved.DefaultModelID != replacement.ID || saved.ProbeModelID != replacement.ID {
					t.Fatalf("repaired selections were not persisted: %+v %v", saved, err)
				}
				if f.count() != 0 {
					t.Fatal("repair dispatched a model request")
				}
			})
		}
	}
}

func TestSaveAgentRejectsNewUnavailableModelSelections(t *testing.T) {
	f, original, provider, replacement := agentModelRecoveryFixture(t)
	if err := f.service.Models.DeleteProvider(f.ctx, provider.ID, provider.Revision); err != nil {
		t.Fatal(err)
	}
	before := f.agent
	for _, change := range []struct {
		name  string
		apply func(*domain.Agent)
	}{
		{"different-default-model", func(agent *domain.Agent) { agent.DefaultModelID = "another-missing-model" }},
		{"different-probe-model", func(agent *domain.Agent) { agent.ProbeModelID = "another-missing-model" }},
		{"default-reasoning", func(agent *domain.Agent) { agent.DefaultReasoning = nil }},
		{"probe-reasoning", func(agent *domain.Agent) { agent.ProbeReasoning = &core.ReasoningOptions{} }},
		{"replacement-with-invalid-reasoning", func(agent *domain.Agent) {
			agent.DefaultModelID = replacement.ID
			agent.DefaultReasoning = &core.ReasoningOptions{Effort: "unsupported"}
		}},
		{"new-bot", func(agent *domain.Agent) { agent.Meta = domain.Meta{} }},
	} {
		t.Run(change.name, func(t *testing.T) {
			candidate := before
			change.apply(&candidate)
			if err := f.service.SaveAgent(f.ctx, &candidate); err == nil {
				t.Fatal("accepted a newly chosen unavailable model/reasoning pair")
			}
			var saved domain.Agent
			if err := f.store.Get(f.ctx, "agents", before.ID, &saved); err != nil || !jsonEqual(saved, before) {
				t.Fatalf("rejected selection changed saved settings: %+v %v", saved, err)
			}
		})
	}
	// Removing an invalid Probe selection is also an explicit valid repair.
	f.agent.ProbeModelID, f.agent.ProbeReasoning = "", nil
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatalf("could not clear unavailable Probe while the default is still unavailable: %v", err)
	}
	if f.agent.DefaultModelID != original.ID || !jsonEqual(f.agent.DefaultReasoning, before.DefaultReasoning) {
		t.Fatal("clearing Probe implicitly changed the default selection")
	}
	if f.count() != 0 {
		t.Fatal("validating model selections dispatched a request")
	}
}

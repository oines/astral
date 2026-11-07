package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestModelPriorityAPIRequiresOwnerAndAppliesOnlyCurrentBot(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("model priority API dispatched inference")
		return testkit.Text("unexpected")
	})
	_, server := apiFixture(t, p)
	replacement := p.Profile
	replacement.Meta = domain.Meta{}
	replacement.ModelID, replacement.Name = "priority-replacement", "Priority replacement"
	replacement.Overrides = map[string]json.RawMessage{"context_window": testkit.JSON(128000), "max_output_tokens": testkit.JSON(4096)}
	if err := p.Harness.Models.SaveProfile(p.Context, &replacement); err != nil {
		t.Fatal(err)
	}
	other := domain.Agent{Name: "Other Bot", DefaultModelID: p.Profile.ID}
	if err := p.Harness.SaveAgent(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	otherSession, err := p.Harness.CreateSession(p.Context, other.ID, "Other conversation")
	if err != nil {
		t.Fatal(err)
	}
	bot := p.Agent
	bot.ModelPriority = []core.ModelSelection{{ID: replacement.ID}, {ID: p.Profile.ID}}
	bot.ModelAutoRestore = true
	response := request(t, p.Context, server, "POST", "/api/v1/agents", strings.NewReader(string(testkit.JSON(bot))), true)
	var saved domain.Agent
	err = json.NewDecoder(response.Body).Decode(&saved)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || saved.DefaultModelID != replacement.ID || len(saved.ModelPriority) != 2 || !saved.ModelAutoRestore {
		t.Fatalf("saving priority: status=%d bot=%+v err=%v", response.StatusCode, saved, err)
	}
	path := "/api/v1/agents/" + bot.ID + "/apply-model-priority"
	response = request(t, p.Context, server, "POST", path, nil, false)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated bulk action: %d", response.StatusCode)
	}
	selection, err := p.Harness.ModelSelection(p.Context, p.Session.ID)
	if err != nil || selection.ID != p.Profile.ID {
		t.Fatal("rejected bulk action changed the conversation")
	}
	response = request(t, p.Context, server, "POST", path, nil, true)
	var applied AppliedModelPriority
	err = json.NewDecoder(response.Body).Decode(&applied)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || applied.Updated != 1 {
		t.Fatalf("bulk action: status=%d result=%+v err=%v", response.StatusCode, applied, err)
	}
	selection, err = p.Harness.ModelSelection(p.Context, p.Session.ID)
	if err != nil || selection.ID != replacement.ID {
		t.Fatal("bulk action did not set current Bot conversation preference")
	}
	selection, err = p.Harness.ModelSelection(p.Context, otherSession.ID)
	if err != nil || selection.ID != p.Profile.ID {
		t.Fatal("bulk action crossed the Bot boundary")
	}
	if p.Requests.Load() != 0 {
		t.Fatal("bulk action started inference")
	}
}

func TestResourceUsageCountsFallbackOnlyBotReferences(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	backup := p.Profile
	backup.Meta = domain.Meta{}
	backup.ModelID, backup.Name = "backup-only", "Backup only"
	backup.Overrides = map[string]json.RawMessage{"context_window": testkit.JSON(128000), "max_output_tokens": testkit.JSON(4096)}
	if err := p.Harness.Models.SaveProfile(p.Context, &backup); err != nil {
		t.Fatal(err)
	}
	bot := p.Agent
	bot.ModelPriority = []core.ModelSelection{{ID: p.Profile.ID}, {ID: backup.ID}}
	if err := p.Harness.SaveAgent(p.Context, &bot); err != nil {
		t.Fatal(err)
	}
	response := request(t, p.Context, server, "GET", "/api/v1/resources/models/"+backup.ID+"/usage", nil, true)
	var usage Usage
	err := json.NewDecoder(response.Body).Decode(&usage)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(usage.Agents) != 1 || usage.Agents[0].ID != bot.ID {
		t.Fatalf("fallback dependency missing: status=%d usage=%+v err=%v", response.StatusCode, usage, err)
	}
}

func TestResourceUsageCountsSessionPreferredAndActiveModelsOnce(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("reading model dependencies dispatched inference")
		return testkit.Text("unexpected")
	})
	_, server := apiFixture(t, p)
	preferred := p.Profile
	preferred.Meta = domain.Meta{}
	preferred.ModelID, preferred.Name = "manual-outside-priority", "Manual preference"
	preferred.Overrides = map[string]json.RawMessage{"context_window": testkit.JSON(128000), "max_output_tokens": testkit.JSON(4096)}
	if err := p.Harness.Models.SaveProfile(p.Context, &preferred); err != nil {
		t.Fatal(err)
	}
	// Represent the durable post-fallback boundary: D was hand-selected
	// outside the Bot's list, while B remains Core's actual executing model.
	state := map[string]any{
		"session_id": p.Session.ID, "agent_id": p.Agent.ID, "phase": "complete",
		"preferred": core.ModelSelection{ID: preferred.ID}, "active": core.ModelSelection{ID: p.Profile.ID},
	}
	if err := p.Store.SaveMaintenance(p.Context, "model_fallback", p.Session.ID, "complete", state); err != nil {
		t.Fatal(err)
	}
	actual, err := p.Harness.ModelSelection(p.Context, p.Session.ID)
	if err != nil || actual.ID != p.Profile.ID {
		t.Fatal("fixture did not retain its active fallback model")
	}
	for _, resource := range []string{"models/" + preferred.ID, "models/" + p.Profile.ID, "providers/" + p.Profile.ProviderID} {
		response := request(t, p.Context, server, "GET", "/api/v1/resources/"+resource+"/usage", nil, true)
		var usage Usage
		err = json.NewDecoder(response.Body).Decode(&usage)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || len(usage.Sessions) != 1 || usage.Sessions[0].ID != p.Session.ID {
			t.Fatalf("dependency %s was omitted or duplicated: status=%d usage=%+v err=%v", resource, response.StatusCode, usage, err)
		}
		if resource == "models/"+preferred.ID && len(usage.Agents) != 0 {
			t.Fatal("fixture's manual preferred model unexpectedly belongs to the Bot priority list")
		}
	}
	if p.Requests.Load() != 0 {
		t.Fatal("dependency reads started inference")
	}
}

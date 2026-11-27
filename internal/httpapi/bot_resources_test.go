package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/schedule"
	"github.com/oines/astral/internal/testkit"
	"github.com/oines/astral/internal/wake"
)

func resourceBotToken(t *testing.T, p *testkit.Platform, server *httptest.Server) string {
	t.Helper()
	path := "/api/v1/agents/" + p.Agent.ID + "/bot-api-tokens"
	response := request(t, p.Context, server, http.MethodPost, path, bytes.NewReader(testkit.JSON(map[string]string{"name": "Resource test"})), true)
	defer response.Body.Close()
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode/100 != 2 || result.Token == "" {
		t.Fatalf("create Bot token: status %d, error %v", response.StatusCode, err)
	}
	return result.Token
}

func resourceBotRequest(t *testing.T, p *testkit.Platform, server *httptest.Server, token, method, path string, body any, status int) []byte {
	t.Helper()
	var input io.Reader
	if body != nil {
		input = bytes.NewReader(testkit.JSON(body))
	}
	req, err := http.NewRequestWithContext(p.Context, method, server.URL+"/api/v1/bot"+path, input)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d, response %s, error %v", method, path, response.StatusCode, status, raw, err)
	}
	return raw
}

func TestBotResourceDirectoryAndSelfPatch(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any {
		t.Error("Bot directory should not call the provider")
		return testkit.Text("unexpected")
	})
	_, server := apiFixture(t, p)
	token := resourceBotToken(t, p, server)
	unassigned := domain.Computer{Meta: domain.Meta{ID: domain.ID("computer")}, Name: "Unassigned", Kind: "remote"}
	enabled := false
	disabled := domain.ModelProfile{
		Name: "Disabled", ProviderID: p.Profile.ProviderID, ModelID: "disabled", Enabled: &enabled,
		Overrides: map[string]json.RawMessage{"context_window": testkit.JSON(64000), "max_output_tokens": testkit.JSON(4096)},
	}
	if err := p.Harness.Models.SaveProfile(p.Context, &disabled); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.Update(p.Context, func(tx *persistence.Tx) error {
		return tx.Save(p.Context, "computers", &unassigned.Meta, &unassigned)
	}); err != nil {
		t.Fatal(err)
	}

	raw := resourceBotRequest(t, p, server, token, "GET", "/self", nil, 200)
	var self BotSelf
	if err := json.Unmarshal(raw, &self); err != nil || self.ID != p.Agent.ID || self.DefaultComputerID != p.Agent.DefaultComputerID {
		t.Fatalf("Bot self: %s, error %v", raw, err)
	}
	if bytes.Contains(raw, []byte(`"allowed_computers"`)) || bytes.Contains(raw, []byte(`"model_priority"`)) {
		t.Fatalf("Bot self leaked Owner settings: %s", raw)
	}
	resourceBotRequest(t, p, server, token, "PATCH", "/self", map[string]any{
		"revision": self.Revision, "allowed_computers": []string{unassigned.ID},
	}, 400)
	resourceBotRequest(t, p, server, token, "PATCH", "/self", map[string]any{
		"revision": self.Revision, "default_computer_id": unassigned.ID,
	}, 400)
	raw = resourceBotRequest(t, p, server, token, "PATCH", "/self", map[string]any{
		"revision": self.Revision, "name": "Renamed Bot", "persona": "Use my preferred language.",
	}, 200)
	var updated BotSelf
	if err := json.Unmarshal(raw, &updated); err != nil || updated.Name != "Renamed Bot" || updated.Persona != "Use my preferred language." || updated.Revision <= self.Revision {
		t.Fatalf("Bot patch: %s, error %v", raw, err)
	}
	var saved domain.Agent
	if err := p.Store.Get(p.Context, "agents", p.Agent.ID, &saved); err != nil || !slices.Equal(saved.AllowedComputers, p.Agent.AllowedComputers) || saved.DefaultComputerID != p.Agent.DefaultComputerID {
		t.Fatalf("Bot grant changed: %+v, error %v", saved, err)
	}
	resourceBotRequest(t, p, server, token, "PATCH", "/self", map[string]any{"revision": self.Revision, "name": "Stale"}, 409)

	raw = resourceBotRequest(t, p, server, token, "GET", "/computers", nil, 200)
	var computers []domain.ComputerStatus
	if err := json.Unmarshal(raw, &computers); err != nil || len(computers) != 1 || computers[0].ID != p.Agent.DefaultComputerID || !computers[0].Online {
		t.Fatalf("Bot computers: %s, error %v", raw, err)
	}
	if bytes.Contains(raw, []byte(`"token_id"`)) || bytes.Contains(raw, []byte(`"connection_ip"`)) {
		t.Fatalf("Bot computers exposed Owner metadata: %s", raw)
	}
	raw = resourceBotRequest(t, p, server, token, "GET", "/models", nil, 200)
	var models []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &models); err != nil || len(models) != 1 || models[0].ID != p.Profile.ID {
		t.Fatalf("Bot models: %s, error %v", raw, err)
	}
	for _, field := range []string{"provider_id", "base_url", "credential_id", "recommendations", "overrides"} {
		if bytes.Contains(raw, []byte(`"`+field+`"`)) {
			t.Fatalf("Bot model directory exposed %s: %s", field, raw)
		}
	}
}

func TestBotSchedulesAndWakeRespectBotAndSessionScope(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unexpected") })
	_, server := apiFixture(t, p)
	token := resourceBotToken(t, p, server)
	otherBot := domain.Agent{Name: "Other Bot", DefaultModelID: p.Profile.ID, AllowedComputers: p.Agent.AllowedComputers, DefaultComputerID: p.Agent.DefaultComputerID}
	if err := p.Harness.SaveAgent(p.Context, &otherBot); err != nil {
		t.Fatal(err)
	}
	foreign, err := p.Harness.CreateSession(p.Context, otherBot.ID, "Foreign Session")
	if err != nil {
		t.Fatal(err)
	}
	private, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "Private Session")
	if err != nil {
		t.Fatal(err)
	}
	hide := true
	if err := p.Harness.SaveSessionPresentation(p.Context, private.ID, nil, nil, nil, &hide); err != nil {
		t.Fatal(err)
	}
	newPlan := func(id string) schedule.Input {
		return schedule.Input{
			SessionID: id, Title: "Daily plan", Prompt: "Follow up",
			Timing: domain.ScheduleTiming{Kind: "cron", Cron: "0 8 * * *"},
		}
	}
	p.Harness.WakeEndpoints.SetComputerURLResolver(func(string) string { return "http://127.0.0.1:32145" })
	for _, target := range []domain.Session{private, foreign} {
		plan, err := p.Harness.Schedules.Create(p.Context, target.AgentID, newPlan(target.ID))
		if err != nil {
			t.Fatal(err)
		}
		resourceBotRequest(t, p, server, token, "GET", "/schedules/"+plan.ID, nil, 404)
		resourceBotRequest(t, p, server, token, "DELETE", fmt.Sprintf("/schedules/%s?revision=%d", plan.ID, plan.Revision), nil, 404)
		resourceBotRequest(t, p, server, token, "POST", "/schedules", newPlan(target.ID), 404)

		source := persistence.WithSessionScope(p.Context, target.AgentID, target.ID)
		endpoint, err := p.Harness.WakeEndpoints.Create(wake.WithComputerID(source, p.Agent.DefaultComputerID), target.AgentID, wake.CreateInput{
			SessionID: target.ID, Title: "Protected event",
		})
		if err != nil {
			t.Fatal(err)
		}
		resourceBotRequest(t, p, server, token, "GET", "/wake-endpoints/"+endpoint.ID, nil, 404)
		resourceBotRequest(t, p, server, token, "DELETE", fmt.Sprintf("/wake-endpoints/%s?revision=%d", endpoint.ID, endpoint.Revision), nil, 404)
	}
	resourceBotRequest(t, p, server, token, "GET", "/schedules?agent_id="+otherBot.ID, nil, 400)
	resourceBotRequest(t, p, server, token, "GET", "/wake-endpoints?agent_id="+otherBot.ID, nil, 400)
	resourceBotRequest(t, p, server, token, "POST", "/schedules", ScheduleCreate{AgentID: otherBot.ID, Input: newPlan(p.Session.ID)}, 400)

	raw := resourceBotRequest(t, p, server, token, "POST", "/schedules", newPlan(p.Session.ID), 200)
	var plan domain.Schedule
	if err := json.Unmarshal(raw, &plan); err != nil || plan.AgentID != p.Agent.ID || plan.SessionID != p.Session.ID {
		t.Fatalf("create plan: %s, error %v", raw, err)
	}
	raw = resourceBotRequest(t, p, server, token, "GET", "/schedules", nil, 200)
	var plans []domain.Schedule
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 || plans[0].ID != plan.ID {
		t.Fatalf("Bot plans: %s, error %v", raw, err)
	}
	edit := ScheduleUpdate{Revision: plan.Revision, Input: newPlan(p.Session.ID)}
	edit.Title = "Updated plan"
	raw = resourceBotRequest(t, p, server, token, "PATCH", "/schedules/"+plan.ID, edit, 200)
	if err := json.Unmarshal(raw, &plan); err != nil || plan.Title != edit.Title {
		t.Fatalf("update plan: %s, error %v", raw, err)
	}
	resourceBotRequest(t, p, server, token, "DELETE", fmt.Sprintf("/schedules/%s?revision=%d", plan.ID, plan.Revision), nil, 200)

	source := persistence.WithSessionScope(p.Context, p.Agent.ID, p.Session.ID)
	endpoint, err := p.Harness.WakeEndpoints.Create(wake.WithComputerID(source, p.Agent.DefaultComputerID), p.Agent.ID, wake.CreateInput{Title: "Bot event"})
	if err != nil {
		t.Fatal(err)
	}
	raw = resourceBotRequest(t, p, server, token, "GET", "/wake-endpoints", nil, 200)
	var endpoints []domain.WakeEndpoint
	if err := json.Unmarshal(raw, &endpoints); err != nil || len(endpoints) != 1 || endpoints[0].ID != endpoint.ID {
		t.Fatalf("Bot wake endpoints: %s, error %v", raw, err)
	}
	title := "Updated event"
	raw = resourceBotRequest(t, p, server, token, "PATCH", "/wake-endpoints/"+endpoint.ID, wake.Patch{Revision: endpoint.Revision, Title: &title}, 200)
	if err := json.Unmarshal(raw, &endpoint); err != nil || endpoint.Title != title {
		t.Fatalf("update wake endpoint: %s, error %v", raw, err)
	}
	resourceBotRequest(t, p, server, token, "DELETE", fmt.Sprintf("/wake-endpoints/%s?revision=%d", endpoint.ID, endpoint.Revision), nil, 200)
}

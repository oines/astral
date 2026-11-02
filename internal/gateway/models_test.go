package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

type modelMetadataHTTP struct{ calls atomic.Int64 }

func (h *modelMetadataHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	h.calls.Add(1)
	return nil, errors.New("model metadata must not contact a provider")
}

func gatewayModelProfile(t *testing.T, p *testkit.Platform, providerID, name string, change func(*domain.ModelProfile)) domain.ModelProfile {
	t.Helper()
	profile := domain.ModelProfile{Meta: domain.Meta{ID: domain.ID("model")}, ProviderID: providerID, Name: name, ModelID: "upstream-" + name,
		Overrides: map[string]json.RawMessage{
			"context_window": testkit.JSON(64000), "max_output_tokens": testkit.JSON(4096), "images": testkit.JSON("unsupported"),
			"private_metadata": testkit.JSON("GATEWAY_MODEL_PRIVATE_METADATA"),
		}}
	if change != nil {
		change(&profile)
	}
	// Store fixtures directly so invalid or formerly valid configurations can
	// coexist with valid entries, as they can after connection changes.
	if err := p.Store.Save(p.Context, "models", &profile.Meta, &profile); err != nil {
		t.Fatal(err)
	}
	return profile
}

func gatewayModelRequest(t *testing.T, ctx context.Context, client *http.Client, method, address string, status int) []byte {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s: status=%d body=%s", method, address, response.StatusCode, body)
	}
	return body
}

func TestAvailableModelsThroughComputerGatewayFiltersInventoryWithoutProviderRequests(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	api := New(p.Harness, channels.New(p.Store, p.Harness))
	p.Hub.Gateway = api.Handle
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := testkit.ComputerAPI(t, p, binding)
	active := gatewayModelProfile(t, p, p.Profile.ProviderID, "another-model", func(profile *domain.ModelProfile) {
		profile.Overrides["max_input_tokens"] = testkit.JSON(32000)
	})
	disabled := false
	gatewayModelProfile(t, p, p.Profile.ProviderID, "disabled-model", func(profile *domain.ModelProfile) { profile.Enabled = &disabled })
	var provider domain.Provider
	if err := p.Store.Get(p.Context, "providers", p.Profile.ProviderID, &provider); err != nil {
		t.Fatal(err)
	}
	provider.Meta, provider.Name, provider.Enabled = domain.Meta{ID: domain.ID("provider")}, "Disabled provider", &disabled
	if err := p.Store.Save(p.Context, "providers", &provider.Meta, &provider); err != nil {
		t.Fatal(err)
	}
	gatewayModelProfile(t, p, provider.ID, "disabled-provider-model", nil)
	gatewayModelProfile(t, p, "missing-provider", "missing-provider-model", nil)
	gatewayModelProfile(t, p, p.Profile.ProviderID, "invalid-model", func(profile *domain.ModelProfile) {
		profile.Overrides["context_window"] = testkit.JSON(-1)
	})
	gatewayModelProfile(t, p, p.Profile.ProviderID, "unknown-limits-model", func(profile *domain.ModelProfile) {
		profile.Overrides["max_output_tokens"] = testkit.JSON(0)
	})
	// An enabled, locally valid connection is listable without resolving its
	// credentials. Neither the absent secret nor its endpoint may be exposed.
	provider.Meta, provider.Name, provider.Enabled = domain.Meta{ID: domain.ID("provider")}, "Offline metadata provider", nil
	provider.BaseURL, provider.CredentialID, provider.HeadersCredentialID = "https://model-metadata.invalid", "unreadable-provider-credential", "unreadable-header-credential"
	if err := p.Store.Save(p.Context, "providers", &provider.Meta, &provider); err != nil {
		t.Fatal(err)
	}
	localOnly := gatewayModelProfile(t, p, provider.ID, "local-only-model", nil)
	noHTTP := &modelMetadataHTTP{}
	p.Harness.Models.HTTPClient = &http.Client{Transport: noHTTP}
	before, err := p.CoreStore.LoadState(p.Context, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	requests := p.Requests.Load()
	body := gatewayModelRequest(t, p.Context, client, "GET", binding.APIURL+"/v1/models", http.StatusOK)
	var inventory []AvailableModel
	if err := json.Unmarshal(body, &inventory); err != nil {
		t.Fatal(err)
	}
	byID := map[string]AvailableModel{}
	for _, model := range inventory {
		byID[model.ID] = model
	}
	if len(byID) != 3 || byID[p.Profile.ID].ID == "" || byID[active.ID].ID == "" || byID[localOnly.ID].ID == "" {
		t.Fatalf("available inventory leaked or omitted models: %+v", inventory)
	}
	if got := byID[active.ID]; got.ModelID != active.ModelID || got.ID == got.ModelID || got.Name != active.Name || got.ProviderName != "test Provider" || got.ContextWindow != 64000 || got.MaxInputTokens != 32000 || got.MaxOutputTokens != 4096 || got.Images != "unsupported" {
		t.Fatalf("effective model metadata was lost: %+v", got)
	}
	if byID[p.Profile.ID].Images != "supported" {
		t.Fatal("inventory lost a model's image capability")
	}
	var fields []map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, model := range fields {
		for _, key := range []string{"provider_id", "base_url", "credential_id", "headers_credential_id", "header_names", "recommendations", "overrides", "recommendation_target", "recommendation_sources", "verification", "revision", "protocol", "dialect"} {
			if _, exists := model[key]; exists {
				t.Errorf("model metadata exposed management field %q", key)
			}
		}
		if string(model["id"]) == string(testkit.JSON(p.Profile.ID)) {
			if _, exists := model["max_input_tokens"]; exists {
				t.Fatal("unknown maximum input limit was presented as a value")
			}
		}
	}
	for _, secret := range []string{"GATEWAY_MODEL_PRIVATE_METADATA", "test-only-provider-key", provider.BaseURL, provider.CredentialID, provider.HeadersCredentialID} {
		if strings.Contains(string(body), secret) {
			t.Errorf("model metadata exposed private configuration %q", secret)
		}
	}
	after, err := p.CoreStore.LoadState(p.Context, p.Session.ID)
	if err != nil || !reflect.DeepEqual(after, before) || p.Requests.Load() != requests || noHTTP.calls.Load() != 0 {
		t.Fatalf("metadata query invoked a provider or changed Session state: requests=%d HTTP=%d error=%v", p.Requests.Load()-requests, noHTTP.calls.Load(), err)
	}
	for _, id := range []string{p.Profile.ID, active.ID, localOnly.ID} {
		var profile domain.ModelProfile
		if err := p.Store.Get(p.Context, "models", id, &profile); err != nil {
			t.Fatal(err)
		}
		profile.Enabled = &disabled
		if err := p.Store.Save(p.Context, "models", &profile.Meta, &profile); err != nil {
			t.Fatal(err)
		}
	}
	if empty := gatewayModelRequest(t, p.Context, client, "GET", binding.APIURL+"/v1/models", http.StatusOK); strings.TrimSpace(string(empty)) != "[]" {
		t.Fatalf("empty available inventory is not an array: %s", empty)
	}
}

func TestAvailableModelsPreserveNativeReasoningValuesBudgetsAndDiagnostics(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	api := New(p.Harness, channels.New(p.Store, p.Harness))
	p.Hub.Gateway = api.Handle
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := testkit.ComputerAPI(t, p, binding)
	qwen := domain.Provider{Meta: domain.Meta{ID: domain.ID("provider")}, Name: "Reasoning metadata", Protocol: "openai-completions", Dialect: "qwen", BaseURL: "https://reasoning-metadata.invalid", ReasoningFormat: models.ReasoningFormatQwen}
	if err := p.Store.Save(p.Context, "providers", &qwen.Meta, &qwen); err != nil {
		t.Fatal(err)
	}
	on, budget := true, 1024
	effortDefault := core.ReasoningOptions{Enabled: &on, Effort: "high"}
	budgetDefault := core.ReasoningOptions{BudgetTokens: &budget}
	efforts := gatewayModelProfile(t, p, qwen.ID, "reasoning-efforts", func(profile *domain.ModelProfile) {
		profile.Overrides["reasoning"] = testkit.JSON(map[string]any{"supported": true, "toggle": true, "disable": true, "efforts": []string{"low", "high"}})
		profile.Overrides["defaults"] = testkit.JSON(core.GenerationOptions{Reasoning: effortDefault})
	})
	budgets := gatewayModelProfile(t, p, qwen.ID, "reasoning-budget", func(profile *domain.ModelProfile) {
		profile.Overrides["reasoning"] = testkit.JSON(map[string]any{"supported": true, "budget": true, "min_budget": 512, "max_budget": 2048})
		profile.Overrides["defaults"] = testkit.JSON(core.GenerationOptions{Reasoning: budgetDefault})
	})
	commandcode := domain.Provider{Meta: domain.Meta{ID: domain.ID("provider")}, Name: "Filtered controls", PresetID: "commandcode", Protocol: "openai-responses", Dialect: "openai", BaseURL: "https://filtered-metadata.invalid"}
	if err := p.Store.Save(p.Context, "providers", &commandcode.Meta, &commandcode); err != nil {
		t.Fatal(err)
	}
	diagnostics := gatewayModelProfile(t, p, commandcode.ID, "reasoning-diagnostics", func(profile *domain.ModelProfile) {
		profile.Overrides["reasoning"] = testkit.JSON(map[string]any{"supported": true, "toggle": true, "disable": true, "efforts": []string{"low", "high", "max"}})
	})
	noHTTP := &modelMetadataHTTP{}
	p.Harness.Models.HTTPClient = &http.Client{Transport: noHTTP}
	var inventory []AvailableModel
	if err := json.Unmarshal(gatewayModelRequest(t, p.Context, client, "GET", binding.APIURL+"/v1/models", http.StatusOK), &inventory); err != nil {
		t.Fatal(err)
	}
	byID := map[string]AvailableModel{}
	for _, model := range inventory {
		byID[model.ID] = model
	}
	for _, profile := range []domain.ModelProfile{efforts, budgets, diagnostics} {
		expected := p.Harness.Models.DescribeProfile(p.Context, profile)
		if got := byID[profile.ID]; expected.Available == nil || !*expected.Available || got.ID == "" || !reflect.DeepEqual(got.ReasoningControls, expected.ReasoningControls) {
			t.Fatalf("native reasoning controls changed for %s: got=%+v want=%+v", profile.Name, got.ReasoningControls, expected.ReasoningControls)
		}
	}
	if !reflect.DeepEqual(byID[efforts.ID].DefaultReasoning, effortDefault) || !reflect.DeepEqual(byID[budgets.ID].DefaultReasoning, budgetDefault) {
		t.Fatal("effective profile reasoning defaults were not preserved")
	}
	controls := byID[efforts.ID].ReasoningControls
	options := map[string]core.ReasoningOptions{}
	for _, option := range controls.Options {
		options[option.ID] = option.Value
	}
	if value, exists := options["interface-default"]; !exists || !reflect.DeepEqual(value, core.ReasoningOptions{}) || options["effort:high"].Effort != "high" || options["on"].Enabled == nil || !*options["on"].Enabled || options["off"].Enabled == nil || *options["off"].Enabled {
		t.Fatalf("reasoning option values were flattened or omitted: %+v", controls)
	}
	budgetControl := byID[budgets.ID].ReasoningControls.Budget
	if budgetControl == nil || budgetControl.Min == nil || *budgetControl.Min != 512 || budgetControl.Max == nil || *budgetControl.Max != 2048 {
		t.Fatalf("reasoning budget bounds were omitted: %+v", budgetControl)
	}
	if len(byID[diagnostics.ID].ReasoningControls.Diagnostics) != 2 || noHTTP.calls.Load() != 0 {
		t.Fatalf("diagnostics were omitted or metadata contacted a provider: %+v HTTP=%d", byID[diagnostics.ID].ReasoningControls, noHTTP.calls.Load())
	}
}

func TestAvailableModelsRequireCommandIdentityAndDoNotExposeManagement(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	api := New(p.Harness, channels.New(p.Store, p.Harness))
	p.Hub.Gateway = api.Handle
	first, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "other command identity")
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Hub.Bind(p.Context, p.Agent.ID, other.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := testkit.ComputerAPI(t, p, first)
	for _, path := range []string{"/v1/models", "/v1/capabilities/models"} {
		gatewayModelRequest(t, p.Context, http.DefaultClient, "GET", first.APIURL+path, http.StatusUnauthorized)
		gatewayModelRequest(t, p.Context, client, "GET", first.APIURL+path, http.StatusOK)
		gatewayModelRequest(t, p.Context, client, "GET", second.APIURL+path, http.StatusUnauthorized)
	}
	for _, operation := range []struct{ method, path string }{
		{"GET", "/api/v1/models"}, {"GET", "/v1/providers"},
		{"POST", "/v1/models/" + p.Profile.ID + "/verify"},
		{"POST", "/v1/providers/" + p.Profile.ProviderID + "/refresh"},
		{"DELETE", "/v1/models/" + p.Profile.ID},
	} {
		gatewayModelRequest(t, p.Context, client, operation.method, first.APIURL+operation.path, http.StatusNotFound)
	}
	forged := first
	forged.SessionID = second.SessionID
	if _, err := api.Handle(p.Context, computers.GatewayRequest{Binding: forged, Method: "GET", Path: "/v1/models"}); err == nil {
		t.Fatal("model metadata accepted a forged command binding")
	}
	p.Agent.AllowedComputers, p.Agent.DefaultComputerID = nil, ""
	if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Handle(p.Context, computers.GatewayRequest{Binding: first, Method: "GET", Path: "/v1/models"}); err == nil {
		t.Fatal("model metadata remained available after source Computer permission was revoked")
	}
}

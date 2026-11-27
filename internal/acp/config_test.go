package acp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/coder/websocket/wsjson"
	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestSessionConfigOptionsUseAvailableModelsAndNativeReasoning(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("done") })
	var original domain.Provider
	if err := p.Store.Get(p.Context, "providers", p.Profile.ProviderID, &original); err != nil {
		t.Fatal(err)
	}
	provider := domain.Provider{Name: "Thinking provider", Dialect: "qwen", Protocol: original.Protocol, BaseURL: original.BaseURL, ReasoningFormat: models.ReasoningFormatQwen}
	if err := p.Harness.Models.SaveProvider(p.Context, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	profile := func(name string, enabled bool, caps map[string]any, defaults core.ReasoningOptions) domain.ModelProfile {
		t.Helper()
		model := domain.ModelProfile{Name: name, ModelID: name, ProviderID: provider.ID, Enabled: &enabled,
			Overrides: map[string]json.RawMessage{
				"context_window": testkit.JSON(32000), "max_output_tokens": testkit.JSON(4096),
				"reasoning": testkit.JSON(caps), "defaults": testkit.JSON(core.GenerationOptions{Reasoning: defaults}),
			},
		}
		if err := p.Harness.Models.SaveProfile(p.Context, &model); err != nil {
			t.Fatal(err)
		}
		return model
	}
	effort := profile("Effort model", true, map[string]any{"supported": true, "toggle": true, "disable": true, "efforts": []string{"low", "high"}}, core.ReasoningOptions{Effort: "high"})
	budget := 768
	budgetModel := profile("Budget model", true, map[string]any{"supported": true, "budget": true, "min_budget": 512, "max_budget": 2048}, core.ReasoningOptions{BudgetTokens: &budget})
	disabled := profile("Disabled model", false, map[string]any{"supported": false}, core.ReasoningOptions{})

	service := New(p.Harness)
	server := httptest.NewServer(service)
	t.Cleanup(server.Close)
	token, err := service.CreateToken(p.Context, p.Agent.ID, "Editor")
	if err != nil {
		t.Fatal(err)
	}
	client, _, err := dial(t, server.URL, token.Token)
	if err != nil {
		t.Fatal(err)
	}
	client.initialize(t)
	type configResponse struct {
		ID     int       `json:"id"`
		Error  *rpcError `json:"error"`
		Result struct {
			SessionID     string                `json:"sessionId"`
			ConfigOptions []sessionConfigOption `json:"configOptions"`
		} `json:"result"`
		Params struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				Kind          string                `json:"sessionUpdate"`
				ConfigOptions []sessionConfigOption `json:"configOptions"`
			} `json:"update"`
		} `json:"params"`
	}
	read := func(id int) configResponse {
		t.Helper()
		for {
			var msg configResponse
			if err := wsjson.Read(client.ctx, client.conn, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.ID == 0 {
				continue
			}
			if msg.ID != id {
				t.Fatalf("received RPC %d, want %d", msg.ID, id)
			}
			return msg
		}
	}
	find := func(options []sessionConfigOption, id string) sessionConfigOption {
		t.Helper()
		index := slices.IndexFunc(options, func(option sessionConfigOption) bool { return option.ID == id })
		if index < 0 {
			t.Fatalf("missing %q in %+v", id, options)
		}
		option := options[index]
		if option.Type != "select" || option.Category != id {
			t.Fatalf("incorrect ACP select metadata: %+v", option)
		}
		return option
	}
	has := func(option sessionConfigOption, value string) bool {
		return slices.ContainsFunc(option.Options, func(choice sessionConfigChoice) bool { return choice.Value == value })
	}
	client.send(t, 2, "session/load", map[string]any{"sessionId": p.Session.ID, "cwd": "/work", "mcpServers": []any{}})
	loaded := read(2)
	if loaded.Error != nil {
		t.Fatalf("load: %+v", loaded.Error)
	}
	model := find(loaded.Result.ConfigOptions, "model")
	if model.CurrentValue != p.Profile.ID || has(model, disabled.ID) || !has(model, effort.ID) || !has(model, budgetModel.ID) {
		t.Fatalf("incorrect model inventory: %+v", model)
	}
	set := func(id int, sessionID, config string, value any) configResponse {
		t.Helper()
		client.send(t, id, "session/set_config_option", map[string]any{"sessionId": sessionID, "configId": config, "value": value})
		return read(id)
	}
	change := func(id int, option string, value string) []sessionConfigOption {
		t.Helper()
		response := set(id, p.Session.ID, option, value)
		if response.Error != nil {
			t.Fatalf("setting %q to %q: %+v", option, value, response.Error)
		}
		return response.Result.ConfigOptions
	}
	options := change(3, "model", effort.ID)
	thought := find(options, "thought_level")
	if find(options, "model").CurrentValue != effort.ID || thought.CurrentValue != "inherit" ||
		!has(thought, "effort:low") || !has(thought, "off") || has(thought, "budget:512") {
		t.Fatalf("incorrect effort controls: %+v", options)
	}
	if got := find(change(4, "thought_level", "effort:low"), "thought_level"); got.CurrentValue != "effort:low" {
		t.Fatalf("effort selection: %+v", got)
	}
	before, err := p.Harness.Snapshot(p.Context, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	change(5, "thought_level", "effort:low")
	after, err := p.Harness.Snapshot(p.Context, p.Session.ID)
	if err != nil || before.State.Version != after.State.Version {
		t.Fatalf("reselecting current effort changed the rollout, or snapshot failed: %v", err)
	}
	if got := find(change(6, "thought_level", "interface-default"), "thought_level"); got.CurrentValue != "interface-default" {
		t.Fatalf("explicit provider default: %+v", got)
	}
	options = change(7, "model", budgetModel.ID)
	thought = find(options, "thought_level")
	if thought.CurrentValue != "inherit" || !has(thought, "budget:512") || !has(thought, "budget:768") || !has(thought, "budget:2048") || has(thought, "effort:low") {
		t.Fatalf("incorrect budget controls: %+v", thought)
	}
	for _, test := range []struct {
		id     int
		config string
		value  any
	}{
		{8, "thought_level", "effort:low"},
		{9, "thought_level", "budget:777"},
		{10, "model", disabled.ID},
		{11, "model", true},
		{12, "unrecognized", "value"},
	} {
		if response := set(test.id, p.Session.ID, test.config, test.value); response.Error == nil || response.Error.Code != -32602 {
			t.Fatalf("expected invalid parameters for %+v, got %+v", test, response.Error)
		}
	}
	if got := find(change(13, "thought_level", "budget:512"), "thought_level"); got.CurrentValue != "budget:512" {
		t.Fatalf("budget selection: %+v", got)
	}
	if err = p.Harness.SelectModelReasoning(p.Context, p.Session.ID, effort.ID, &core.ReasoningOptions{Effort: "high"}); err == nil {
		t.Fatal("a stale reasoning edit replaced the current model")
	}
	session, err := p.Harness.Core(p.Context, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	selection := session.Configuration().Selection
	if selection.ID != budgetModel.ID || selection.Reasoning == nil || selection.Reasoning.BudgetTokens == nil || *selection.Reasoning.BudgetTokens != 512 {
		t.Fatalf("Host selection = %+v", selection)
	}
	if response := set(14, "not-loaded", "model", effort.ID); response.Error == nil || response.Error.Code != -32002 {
		t.Fatalf("unloaded session error: %+v", response.Error)
	}

	// Display a valid custom budget saved by an existing UI even when new
	// choices in ACP are limited to presets.
	custom := 777
	if err = p.Harness.SelectModel(p.Context, p.Session.ID, budgetModel.ID, &core.ReasoningOptions{BudgetTokens: &custom}); err != nil {
		t.Fatal(err)
	}
	for {
		var msg configResponse
		if err := wsjson.Read(client.ctx, client.conn, &msg); err != nil {
			t.Fatalf("waiting for external config update: %v", err)
		}
		if msg.Params.SessionID == p.Session.ID && msg.Params.Update.Kind == "config_option_update" &&
			find(msg.Params.Update.ConfigOptions, "thought_level").CurrentValue == "budget:777" {
			break
		}
	}
	client.send(t, 15, "session/load", map[string]any{"sessionId": p.Session.ID, "cwd": "/work", "mcpServers": []any{}})
	loaded = read(15)
	if loaded.Error != nil {
		t.Fatalf("reload: %+v", loaded.Error)
	}
	thought = find(loaded.Result.ConfigOptions, "thought_level")
	if thought.CurrentValue != "budget:777" || !has(thought, "budget:777") {
		t.Fatalf("existing budget omitted: %+v", thought)
	}
}

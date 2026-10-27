package modelbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	models "github.com/oines/astral-core/models"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func reasoningIntegrationBridge(t *testing.T) (*Bridge, string, <-chan map[string]any) {
	t.Helper()
	requests := make(chan map[string]any, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/responses") {
			_, _ = w.Write([]byte(`{"id":"response-1","status":"completed","output":[{"type":"message","id":"message-1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chat-1","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Bridge{Store: store, HTTPClient: server.Client()}, server.URL, requests
}

func reasoningIntegrationProfile(provider domain.Provider, modelID string, caps any) domain.ModelProfile {
	return domain.ModelProfile{
		ProviderID: provider.ID, ModelID: modelID, Name: modelID,
		Recommendations: map[string]json.RawMessage{
			"context_window": raw(128000), "max_output_tokens": raw(8192), "streaming": raw(false), "reasoning": raw(caps),
		},
		Overrides: map[string]json.RawMessage{},
	}
}

func reasoningIntegrationCall(t *testing.T, bridge *Bridge, profile domain.ModelProfile, selection core.ReasoningOptions, requests <-chan map[string]any) map[string]any {
	t.Helper()
	ctx := context.Background()
	ref, _, err := bridge.Selection(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	model, err := bridge.Resolve(ctx, ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Stream(ctx, core.Request{
		ModelID: ref, ModelRevision: ref, MaxOutputTokens: 4096,
		Messages: []core.Message{core.User("hello")}, Options: core.GenerationOptions{Reasoning: selection},
	}, func(core.ModelEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-requests:
		return body
	default:
		t.Fatal("model returned without sending a request")
		return nil
	}
}

func TestReasoningIntegrationFormatPrecedence(t *testing.T) {
	tests := []struct {
		name, preset, dialect string
		providerFormat        models.ReasoningFormat
		modelFormat           models.ReasoningFormat
		want                  models.ReasoningFormat
	}{
		{"model override", "qwen", "qwen", models.ReasoningFormatThinkingType, models.ReasoningFormatChatTemplate, models.ReasoningFormatChatTemplate},
		{"provider override", "qwen", "qwen", models.ReasoningFormatThinkingType, "", models.ReasoningFormatThinkingType},
		{"preset binding", "qwen", "compatible", "", "", models.ReasoningFormatQwen},
		{"explicit protocol default", "qwen", "qwen", models.ReasoningFormatProtocolDefault, "", models.ReasoningFormatOpenAIChat},
		{"custom protocol default", "", "deepseek", "", "", models.ReasoningFormatOpenAIChat},
		{"new custom standard protocol", "", "deepseek", models.ReasoningFormatProtocolDefault, "", models.ReasoningFormatOpenAIChat},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bridge, baseURL, _ := reasoningIntegrationBridge(t)
			ctx := context.Background()
			provider := domain.Provider{Name: test.name, PresetID: test.preset, Protocol: "openai-completions", Dialect: test.dialect, BaseURL: baseURL, ReasoningFormat: test.providerFormat}
			if err := bridge.SaveProvider(ctx, &provider, nil, nil); err != nil {
				t.Fatal(err)
			}
			profile := reasoningIntegrationProfile(provider, "gateway/mimo-v2.6-pro", map[string]any{"supported": true, "efforts": []string{"high"}})
			if test.modelFormat != "" {
				profile.Overrides["reasoning_format"] = raw(test.modelFormat)
			}
			if err := bridge.SaveProfile(ctx, &profile); err != nil {
				t.Fatal(err)
			}
			described := bridge.DescribeProfile(ctx, profile)
			if described.ReasoningError != "" || described.ReasoningControls == nil || described.ReasoningControls.Format != test.want {
				t.Fatalf("controls = %+v, error = %q; want %q", described.ReasoningControls, described.ReasoningError, test.want)
			}
		})
	}
}

func TestReasoningIntegrationCustomSlugKeepsProtocolWire(t *testing.T) {
	for _, modelID := range []string{"org/deepseek-v4-pro", "org/qwen3.8-max", "org/xiaomi/mimo-v2.6-pro", "org/claude-sonnet-4-6"} {
		t.Run(modelID, func(t *testing.T) {
			bridge, baseURL, requests := reasoningIntegrationBridge(t)
			ctx := context.Background()
			provider := domain.Provider{Name: "custom", Protocol: "openai-completions", Dialect: "compatible", BaseURL: baseURL, ReasoningFormat: models.ReasoningFormatProtocolDefault}
			if err := bridge.SaveProvider(ctx, &provider, nil, nil); err != nil {
				t.Fatal(err)
			}
			profile := reasoningIntegrationProfile(provider, modelID, map[string]any{"supported": true, "efforts": []string{"high"}})
			if err := bridge.SaveProfile(ctx, &profile); err != nil {
				t.Fatal(err)
			}
			body := reasoningIntegrationCall(t, bridge, profile, core.ReasoningOptions{Effort: "high"}, requests)
			if body["model"] != modelID || body["reasoning_effort"] != "high" {
				t.Fatalf("model identity or standard effort changed: %v", body)
			}
			for _, field := range []string{"thinking", "enable_thinking", "chat_template_kwargs", "reasoning", "output_config"} {
				if _, exists := body[field]; exists {
					t.Errorf("slug introduced foreign field %s", field)
				}
			}
		})
	}
}

func TestReasoningIntegrationMiMoResponsesOnlyExposesSwitch(t *testing.T) {
	bridge, baseURL, requests := reasoningIntegrationBridge(t)
	ctx := context.Background()
	provider := domain.Provider{Name: "MiMo", PresetID: "mimo", Protocol: "openai-responses", Dialect: "openai", BaseURL: baseURL}
	if err := bridge.SaveProvider(ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	// MiMo accepts several effort spellings but documents identical enabled
	// behavior. This is a switch contract, not a list of strength levels.
	profile := reasoningIntegrationProfile(provider, "mimo-v2.6-pro", map[string]any{
		"supported": true, "toggle": true, "disable": true, "default_on": true,
		"efforts": []string{}, "on_effort": "high", "off_effort": "none",
	})
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	described := bridge.DescribeProfile(ctx, profile)
	if described.ReasoningError != "" || described.ReasoningControls == nil {
		t.Fatalf("controls: %+v", described)
	}
	on, off := false, false
	for _, option := range described.ReasoningControls.Options {
		if option.Value.Effort != "" || option.Value.BudgetTokens != nil {
			t.Fatalf("switch exposed a fictional level: %+v", option)
		}
		if option.Value.Enabled != nil {
			if *option.Value.Enabled {
				on = true
			} else {
				off = true
			}
		}
	}
	if !on || !off || described.ReasoningControls.Budget != nil {
		t.Fatalf("expected independent on/off controls: %+v", described.ReasoningControls)
	}
	for _, enabled := range []bool{true, false} {
		body := reasoningIntegrationCall(t, bridge, profile, core.ReasoningOptions{Enabled: &enabled}, requests)
		want := "none"
		if enabled {
			want = "high"
		}
		if !reflect.DeepEqual(body["reasoning"], map[string]any{"effort": want}) {
			t.Fatalf("enabled=%v body=%v", enabled, body)
		}
		if _, exists := body["thinking"]; exists {
			t.Fatal("Chat thinking field escaped into Responses")
		}
	}
}

func TestReasoningIntegrationSameModelDifferentConnections(t *testing.T) {
	tests := []struct {
		preset, protocol, dialect string
		want                      map[string]any
	}{
		{"mimo", "openai-completions", "compatible", map[string]any{"thinking": map[string]any{"type": "enabled"}}},
		{"mimo", "openai-responses", "openai", map[string]any{"reasoning": map[string]any{"effort": "high"}}},
		{"qwen", "openai-completions", "qwen", map[string]any{"enable_thinking": true}},
		{"together", "openai-completions", "compatible", map[string]any{"reasoning": map[string]any{"enabled": true}}},
	}
	for _, test := range tests {
		t.Run(test.preset+"/"+test.protocol, func(t *testing.T) {
			bridge, baseURL, requests := reasoningIntegrationBridge(t)
			ctx := context.Background()
			provider := domain.Provider{Name: test.preset, PresetID: test.preset, Protocol: test.protocol, Dialect: test.dialect, BaseURL: baseURL}
			if err := bridge.SaveProvider(ctx, &provider, nil, nil); err != nil {
				t.Fatal(err)
			}
			// Each fixture declares its own route capabilities; the model ID
			// being identical does not share wire aliases between connections.
			caps := map[string]any{"supported": true, "toggle": true, "disable": true, "efforts": []string{}}
			if test.protocol == "openai-responses" {
				caps["on_effort"], caps["off_effort"] = "high", "none"
			}
			profile := reasoningIntegrationProfile(provider, "mimo-v2.6-pro", caps)
			if err := bridge.SaveProfile(ctx, &profile); err != nil {
				t.Fatal(err)
			}
			enabled := true
			body := reasoningIntegrationCall(t, bridge, profile, core.ReasoningOptions{Enabled: &enabled}, requests)
			if body["model"] != "mimo-v2.6-pro" {
				t.Fatalf("changed remote model ID: %v", body["model"])
			}
			for _, field := range []string{"thinking", "enable_thinking", "reasoning", "reasoning_effort", "chat_template_kwargs"} {
				if !reflect.DeepEqual(body[field], test.want[field]) {
					t.Errorf("%s = %v, want %v", field, body[field], test.want[field])
				}
			}
		})
	}
}

func TestReasoningIntegrationMappingCannotReplaceRequestContract(t *testing.T) {
	for _, field := range []string{"model", "messages", "tools", "stream", "max_tokens", "authorization", "generationConfig.maxOutputTokens"} {
		t.Run(field, func(t *testing.T) {
			bridge, baseURL, _ := reasoningIntegrationBridge(t)
			fragment := map[string]any{field: "forbidden"}
			if field == "generationConfig.maxOutputTokens" {
				fragment = map[string]any{"generationConfig": map[string]any{"maxOutputTokens": 100}}
			}
			mapping := &models.ReasoningMapping{On: raw(fragment)}
			provider := domain.Provider{Name: "custom", Protocol: "openai-completions", Dialect: "compatible", BaseURL: baseURL, ReasoningFormat: models.ReasoningFormatCustom, ReasoningMapping: mapping}
			if err := bridge.SaveProvider(context.Background(), &provider, nil, nil); err == nil {
				t.Fatal("provider accepted mapping that replaces " + field)
			}
			provider.ReasoningFormat, provider.ReasoningMapping = models.ReasoningFormatProtocolDefault, nil
			if err := bridge.SaveProvider(context.Background(), &provider, nil, nil); err != nil {
				t.Fatal(err)
			}
			profile := reasoningIntegrationProfile(provider, "remote", nil)
			profile.Overrides["reasoning_format"] = raw(models.ReasoningFormatCustom)
			profile.Overrides["reasoning_mapping"] = raw(mapping)
			if err := bridge.SaveProfile(context.Background(), &profile); err == nil {
				t.Fatal("model accepted mapping that replaces " + field)
			}
		})
	}
}

func TestReasoningIntegrationModelMappingReplacesProviderMapping(t *testing.T) {
	bridge, baseURL, requests := reasoningIntegrationBridge(t)
	ctx := context.Background()
	provider := domain.Provider{Name: "custom", Protocol: "openai-completions", Dialect: "compatible", BaseURL: baseURL,
		ReasoningFormat:  models.ReasoningFormatCustom,
		ReasoningMapping: &models.ReasoningMapping{On: raw(map[string]any{"thinking": map[string]any{"type": "enabled"}})},
	}
	if err := bridge.SaveProvider(ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	profile := reasoningIntegrationProfile(provider, "unchanged-model-id", nil)
	profile.Overrides["reasoning_mapping"] = raw(models.ReasoningMapping{On: raw(map[string]any{"custom_reasoning": "on"})})
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	enabled := true
	body := reasoningIntegrationCall(t, bridge, profile, core.ReasoningOptions{Enabled: &enabled}, requests)
	if body["custom_reasoning"] != "on" || body["thinking"] != nil || body["model"] != "unchanged-model-id" {
		t.Fatalf("model mapping was merged with provider mapping or replaced model: %v", body)
	}
}

func TestReasoningIntegrationIgnoresClientSuppliedControls(t *testing.T) {
	bridge, baseURL, _ := reasoningIntegrationBridge(t)
	ctx := context.Background()
	provider := domain.Provider{Name: "custom", Protocol: "openai-completions", Dialect: "compatible", BaseURL: baseURL, ReasoningFormat: models.ReasoningFormatProtocolDefault}
	if err := bridge.SaveProvider(ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	profile := reasoningIntegrationProfile(provider, "remote", map[string]any{"supported": true, "efforts": []string{"high"}})
	profile.ReasoningControls = &models.ReasoningControls{Format: models.ReasoningFormatQwen, Options: []models.ReasoningOption{{ID: "invented", Value: core.ReasoningOptions{Effort: "invented"}}}}
	profile.ReasoningError = "forged error"
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	var stored domain.ModelProfile
	if err := bridge.Store.Get(ctx, "models", profile.ID, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ReasoningControls != nil || stored.ReasoningError != "" {
		t.Fatal("server persisted client-supplied derived controls")
	}
	described := bridge.DescribeProfile(ctx, stored)
	if described.ReasoningError != "" || described.ReasoningControls == nil || described.ReasoningControls.Format != models.ReasoningFormatOpenAIChat {
		t.Fatalf("server did not derive controls from saved settings: %+v", described)
	}
	for _, option := range described.ReasoningControls.Options {
		if option.Value.Effort != "" && option.Value.Effort != "high" {
			t.Fatalf("forged effort entered derived controls: %+v", option)
		}
	}
	ref, _, err := bridge.Selection(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.ValidateOptions(ctx, ref, core.GenerationOptions{Reasoning: core.ReasoningOptions{Effort: "invented"}}); err == nil {
		t.Fatal("client-supplied controls authorized an unsupported effort")
	}
}

func TestDescribeProfileKeepsReasoningControlsWhenTokenLimitsAreUnknown(t *testing.T) {
	for _, missing := range []string{"context_window", "max_output_tokens", "both"} {
		t.Run(missing, func(t *testing.T) {
			bridge, baseURL, requests := reasoningIntegrationBridge(t)
			ctx := context.Background()
			connection := domain.Provider{Name: "custom", Protocol: "openai-responses", Dialect: "openai", BaseURL: baseURL}
			if err := bridge.SaveProvider(ctx, &connection, nil, nil); err != nil {
				t.Fatal(err)
			}
			profile := reasoningIntegrationProfile(connection, "vendor/model", map[string]any{"supported": true, "efforts": []string{"high"}})
			if missing == "both" {
				profile.Recommendations["context_window"] = raw(0)
				delete(profile.Recommendations, "max_output_tokens")
			} else {
				delete(profile.Recommendations, missing)
			}
			profile.ID = domain.ID("model")
			// Catalog refresh may persist sparse metadata before the user fills
			// its limits; describing it must not require constructing a request.
			if err := bridge.Store.Save(ctx, "models", &profile.Meta, &profile); err != nil {
				t.Fatal(err)
			}
			described := bridge.DescribeProfile(ctx, profile)
			if described.ReasoningError != "" || described.ReasoningControls == nil {
				t.Fatalf("unknown limits hid reasoning controls: %+v", described)
			}
			var ids []string
			for _, option := range described.ReasoningControls.Options {
				ids = append(ids, option.ID)
			}
			if !reflect.DeepEqual(ids, []string{"interface-default", "effort:high"}) {
				t.Fatalf("reasoning choices = %v", ids)
			}
			if described.Available == nil || *described.Available || described.UnavailableReason != "token_limits_unknown" {
				t.Fatalf("unknown limits were considered runnable: %+v", described)
			}
			if available, reason := bridge.Availability(ctx, profile.ID); available || reason != "token_limits_unknown" {
				t.Fatalf("availability = %v, %q", available, reason)
			}
			select {
			case <-requests:
				t.Fatal("describing metadata called the model")
			default:
			}
		})
	}
}

func TestDescribeProfileFilteredReasoningCandidatesDoNotInvalidateAPIDefault(t *testing.T) {
	bridge, baseURL, requests := reasoningIntegrationBridge(t)
	ctx := context.Background()
	connection := domain.Provider{Name: "gateway", PresetID: "commandcode", Protocol: "openai-responses", Dialect: "openai", BaseURL: baseURL}
	if err := bridge.SaveProvider(ctx, &connection, nil, nil); err != nil {
		t.Fatal(err)
	}
	profile := reasoningIntegrationProfile(connection, "vendor/model", map[string]any{
		"supported": true, "toggle": true, "disable": true, "efforts": []string{"low", "high", "max"},
	})
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	described := bridge.DescribeProfile(ctx, profile)
	if described.ReasoningError != "" || described.Available == nil || !*described.Available || described.ReasoningControls == nil {
		t.Fatalf("filtered candidates invalidated the API default: %+v", described)
	}
	var ids []string
	for _, option := range described.ReasoningControls.Options {
		ids = append(ids, option.ID)
	}
	if !reflect.DeepEqual(ids, []string{"interface-default", "effort:low", "effort:high", "effort:max"}) || len(described.ReasoningControls.Diagnostics) != 2 {
		t.Fatalf("controls = %+v", described.ReasoningControls)
	}
	if _, err := bridge.ResolveSelection(ctx, core.ModelSelection{ID: profile.ID, Reasoning: &core.ReasoningOptions{}}); err != nil {
		t.Fatalf("explicit API default is invalid: %v", err)
	}
	select {
	case <-requests:
		t.Fatal("describing or resolving metadata called the model")
	default:
	}
}

func TestSaveProfileStillRequiresAgentTokenLimits(t *testing.T) {
	bridge, baseURL, _ := reasoningIntegrationBridge(t)
	ctx := context.Background()
	connection := domain.Provider{Name: "custom", Protocol: "openai-responses", Dialect: "openai", BaseURL: baseURL}
	if err := bridge.SaveProvider(ctx, &connection, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"context_window", "max_output_tokens"} {
		profile := reasoningIntegrationProfile(connection, "vendor/model", map[string]any{"supported": true, "efforts": []string{"high"}})
		delete(profile.Recommendations, missing)
		if err := bridge.SaveProfile(ctx, &profile); err == nil || !strings.Contains(err.Error(), "token limits must be positive") {
			t.Fatalf("saving without %s returned %v", missing, err)
		}
	}
	if records, err := bridge.Store.List(ctx, "models"); err != nil || len(records) != 0 {
		t.Fatalf("rejected models were persisted: %v, %v", records, err)
	}
}

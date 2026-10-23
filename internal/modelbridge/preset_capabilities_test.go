package modelbridge

import (
	"encoding/json"
	"reflect"
	"testing"

	core "github.com/oines/astral-core"
	models "github.com/oines/astral-models"
	"github.com/oines/astral/internal/domain"
)

func presetCapabilitiesConfig(t *testing.T, preset, modelID, protocol string) models.Config {
	t.Helper()
	values, source := presetReasoningRecommendations(domain.Provider{PresetID: preset}, modelID, protocol)
	if len(values) == 0 || source == "" {
		t.Fatalf("missing audited contract for %s / %s / %s", preset, modelID, protocol)
	}
	var caps models.ReasoningCapabilities
	if err := json.Unmarshal(raw(values), &caps); err != nil {
		t.Fatal(err)
	}
	return models.Config{Reasoning: &caps, ReasoningFormat: presetReasoningFormat(preset, protocol, modelID, &caps)}
}

func TestPresetCapabilitiesDoNotInferFromSlugOrUnknownRoute(t *testing.T) {
	for _, test := range []struct{ preset, modelID, protocol string }{
		{"", "mimo-v2.6-pro", "openai-responses"},
		{"newapi", "mimo-v2.6-pro", "openai-responses"},
		{"openrouter", "deepseek/deepseek-v4-pro", "openai-completions"},
		{"azure-openai", "gpt-6.1-sol", "openai-responses"},
		{"mimo", "vendor/mimo-v2.6-pro", "openai-responses"},
		{"mimo", "mimo-v2.7-pro", "openai-responses"},
		{"mimo", "mimo-v2.6-pro", "anthropic-messages"},
		{"google", "gemini-2.5-pro", "openai-completions"},
		{"anthropic", "claude-future", "anthropic-messages"},
	} {
		values, source := presetReasoningRecommendations(domain.Provider{PresetID: test.preset}, test.modelID, test.protocol)
		if values != nil || source != "" {
			t.Errorf("unaudited route %+v received facts %s from %s", test, raw(values), source)
		}
	}
}

func TestPresetCapabilitiesDescribeNativeControls(t *testing.T) {
	for _, test := range []struct {
		preset, modelID, protocol string
		efforts                   []string
		disable, budget           bool
	}{
		{"mimo", "mimo-v2.6-pro", "openai-responses", []string{}, true, false},
		{"deepseek", "deepseek-v4-pro", "openai-completions", []string{"low", "high", "max"}, true, false},
		{"moonshot", "kimi-k3", "openai-completions", []string{"low", "high", "max"}, false, false},
		{"moonshot", "kimi-k2.6", "openai-completions", []string{}, true, false},
		{"moonshot", "kimi-k2.7-code", "openai-completions", []string{}, false, false},
		{"zai", "glm-5.3", "openai-completions", []string{"low", "high", "max"}, false, false},
		{"qwen", "deepseek-v4-pro", "openai-completions", nil, true, false},
		{"qwen", "qwen3.8-max", "openai-responses", []string{"low", "medium", "xhigh"}, true, false},
		{"qwen", "deepseek-v4-pro", "openai-responses", []string{"high", "max"}, true, false},
		{"zhipu", "glm-5.3", "openai-responses", []string{"high", "max"}, true, false},
		{"moonshot", "kimi-k3", "openai-responses", []string{"low", "high", "max"}, false, false},
		{"google", "gemini-2.5-pro", "google-generative-ai", []string{}, false, true},
		{"google", "gemini-2.5-flash-lite", "google-generative-ai", []string{}, true, true},
		{"google", "gemini-3.6-flash", "google-generative-ai", []string{"minimal", "low", "medium", "high"}, false, false},
		{"minimax", "MiniMax-M3", "anthropic-messages", []string{}, true, false},
		{"minimax", "MiniMax-M3.1-Flash", "anthropic-messages", []string{"low", "medium", "high", "xhigh", "max"}, false, false},
		{"anthropic", "claude-opus-4-6", "anthropic-messages", []string{"low", "medium", "high", "max"}, true, true},
		{"anthropic", "claude-sonnet-4-5", "anthropic-messages", []string{}, true, true},
		{"openai", "gpt-6.1-sol", "openai-responses", []string{"low", "medium", "high", "xhigh", "max"}, false, false},
		{"xai", "grok-4.5", "openai-responses", []string{"low", "medium", "high"}, false, false},
	} {
		t.Run(test.preset+"/"+test.modelID, func(t *testing.T) {
			cfg := presetCapabilitiesConfig(t, test.preset, test.modelID, test.protocol)
			if !reflect.DeepEqual(cfg.Reasoning.Efforts, test.efforts) {
				t.Fatalf("efforts = %#v, want %#v", cfg.Reasoning.Efforts, test.efforts)
			}
			if cfg.Reasoning.Disable == nil || *cfg.Reasoning.Disable != test.disable || cfg.Reasoning.Budget == nil || *cfg.Reasoning.Budget != test.budget {
				t.Fatalf("unexpected disable/budget: %s", raw(cfg.Reasoning))
			}
			controls, err := models.ResolveReasoningControls(test.protocol, cfg)
			if err != nil {
				t.Fatal(err)
			}
			for _, option := range controls.Options {
				if _, err := models.EncodeReasoning(test.protocol, cfg, option.Value, 8192); err != nil {
					t.Errorf("offered option %s cannot encode: %v", option.ID, err)
				}
			}
		})
	}
}

func TestPresetCapabilitiesMiMoSwitchDoesNotInventIntensity(t *testing.T) {
	cfg := presetCapabilitiesConfig(t, "mimo", "mimo-v2.6-pro", "openai-responses")
	controls, err := models.ResolveReasoningControls("openai-responses", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(controls.Options) != 3 { // Interface default, off, on.
		t.Fatalf("MiMo controls = %s", raw(controls))
	}
	for _, option := range controls.Options {
		if option.Value.Effort != "" {
			t.Fatalf("equivalent wire alias exposed as intensity: %s", raw(option))
		}
		encoded, err := models.EncodeReasoning("openai-responses", cfg, option.Value, 8192)
		if err != nil {
			t.Fatal(err)
		}
		if option.Value.Enabled != nil {
			want := "none"
			if *option.Value.Enabled {
				want = "high"
			}
			if !reflect.DeepEqual(encoded.Fields, map[string]any{"reasoning": map[string]any{"effort": want}}) {
				t.Errorf("switch wire = %#v, want effort %s", encoded.Fields, want)
			}
		}
	}
}

func TestPresetCapabilitiesBudgetOffIsNotMinimumThinking(t *testing.T) {
	cfg := presetCapabilitiesConfig(t, "google", "gemini-2.5-flash-lite", "google-generative-ai")
	off := false
	encoded, err := models.EncodeReasoning("google-generative-ai", cfg, core.ReasoningOptions{Enabled: &off}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": 0}}}
	if !reflect.DeepEqual(encoded.Fields, want) {
		t.Fatalf("off wire = %#v, want %#v", encoded.Fields, want)
	}
	tooLow := 511
	if _, err := models.EncodeReasoning("google-generative-ai", cfg, core.ReasoningOptions{BudgetTokens: &tooLow}, 8192); err == nil {
		t.Fatal("positive thinking budget below the minimum was accepted")
	}
}

package modelbridge

import (
	"testing"

	models "github.com/oines/astral-models"
)

func TestEveryPresetHasAuditedReasoningBinding(t *testing.T) {
	seen := make(map[string]bool)
	for _, preset := range ProviderPresets() {
		seen[preset.ID] = true
		binding, ok := presetReasoningBindings[preset.ID]
		if !ok || binding == "" {
			t.Errorf("preset %s needs an explicit reasoning audit and binding", preset.ID)
			continue
		}
		if preset.ReasoningFormat != binding {
			t.Errorf("preset %s format = %q, want audited %q", preset.ID, preset.ReasoningFormat, binding)
		}
		if actual := presetReasoningFormat(preset.ID, preset.Protocol, "", nil); actual != binding {
			t.Errorf("preset %s resolves %q for its default protocol, want %q", preset.ID, actual, binding)
		}
	}
	for id := range presetReasoningBindings {
		if !seen[id] {
			t.Errorf("reasoning audit retains removed preset %s", id)
		}
	}
}

func TestPresetReasoningRespectsCurrentProtocol(t *testing.T) {
	for _, id := range []string{"deepseek", "qwen", "openrouter", "together", "fireworks", "mimo", "moonshot", "minimax", "unknown"} {
		for protocol, want := range map[string]models.ReasoningFormat{
			"openai-responses":     models.ReasoningFormatOpenAIResponses,
			"google-generative-ai": models.ReasoningFormatGemini,
		} {
			if got := presetReasoningFormat(id, protocol, "kimi-k3", nil); got != want {
				t.Errorf("%s/%s = %q, want %q", id, protocol, got, want)
			}
		}
	}
}

func TestPresetReasoningUsesGatewayContract(t *testing.T) {
	tests := []struct {
		preset, model string
		want          models.ReasoningFormat
	}{
		{"deepseek", "deepseek-v4-pro", models.ReasoningFormatThinkingType},
		{"qwen", "deepseek-v4-pro", models.ReasoningFormatQwen},
		{"together", "deepseek-ai/DeepSeek-V4-Pro-0813", models.ReasoningFormatTogether},
		{"fireworks", "accounts/fireworks/models/deepseek-v4-pro", models.ReasoningFormatFireworks},
		{"openrouter", "deepseek/deepseek-v4-pro", models.ReasoningFormatOpenRouter},
		{"cerebras", "zai-glm-4.7", models.ReasoningFormatOpenAIChat},
		{"zai", "glm-4.7", models.ReasoningFormatThinkingType},
		{"moonshot", "kimi-k2.6", models.ReasoningFormatThinkingType},
		{"moonshot", "kimi-k3", models.ReasoningFormatOpenAIChat},
		{"kimi-code", "k3", models.ReasoningFormatOpenAIChat},
		{"custom", "vendor/qwen3.8-max", models.ReasoningFormatOpenAIChat},
		{"custom", "vendor/deepseek-v4-pro", models.ReasoningFormatOpenAIChat},
		{"custom", "vendor/kimi-k2.6", models.ReasoningFormatOpenAIChat},
	}
	for _, test := range tests {
		if got := presetReasoningFormat(test.preset, "openai-completions", test.model, nil); got != test.want {
			t.Errorf("%s/%s = %q, want %q", test.preset, test.model, got, test.want)
		}
	}
}

func TestPresetReasoningDoesNotTurnCapabilitiesIntoWireFormat(t *testing.T) {
	no := false
	caps := &models.ReasoningCapabilities{Toggle: &no, Efforts: []string{"low", "high", "max"}}
	// Capabilities from a matching model catalogue must not change the request
	// format of an unknown intermediary or a different selected protocol.
	if got := presetReasoningFormat("", "openai-responses", "somewhere/kimi-k3", caps); got != models.ReasoningFormatOpenAIResponses {
		t.Fatalf("catalogue capabilities changed a Responses route to %q", got)
	}
	if got := presetReasoningFormat("", "openai-completions", "somewhere/kimi-k3", caps); got != models.ReasoningFormatOpenAIChat {
		t.Fatalf("catalogue capabilities changed a Chat route to %q", got)
	}
}

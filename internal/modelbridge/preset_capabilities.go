package modelbridge

import (
	"encoding/json"

	"github.com/oines/astral/internal/domain"
)

// presetReasoningRecommendations records published control contracts, not a
// model inventory. Match the complete remote ID: a similarly named model on a
// gateway, a future release, or another protocol may have different controls.
// Live discovery overrides these facts, including explicit false/empty values.
// Missing fields remain unknown; they are not inferred from a model family.
func presetReasoningRecommendations(provider domain.Provider, modelID, protocol string) (map[string]json.RawMessage, string) {
	var values map[string]any
	var source string
	switch provider.PresetID {
	case "fireworks":
		if protocol == "anthropic-messages" {
			// The endpoint accepts a budget syntactically but converts it to
			// an effort band. Do not expose an exact token-budget control.
			values = map[string]any{"budget": false, "dynamic_budget": false}
			source = "https://docs.fireworks.ai/tools-sdks/anthropic-compatibility"
		}
	case "openai":
		if protocol != "openai-responses" && protocol != "openai-completions" {
			break
		}
		switch modelID {
		case "gpt-6.1-sol":
			values = reasoningLevels([]string{"low", "medium", "high", "xhigh", "max"}, "medium")
			source = "https://developers.openai.com/api/docs/models/gpt-6.1-sol"
		case "gpt-6-astra":
			values = reasoningLevels([]string{"low", "medium", "high", "xhigh", "max"}, "")
			delete(values, "default_effort")
			source = "https://developers.openai.com/api/docs/models/gpt-6-astra"
		}
	case "anthropic":
		if protocol != "anthropic-messages" {
			break
		}
		switch modelID {
		case "claude-opus-5-5":
			values = reasoningLevels([]string{"low", "medium", "high", "xhigh", "max"}, "medium")
		case "claude-sonnet-5-5", "claude-fable-5-1", "claude-mythos-5-1", "claude-fable-5", "claude-mythos-5":
			values = reasoningLevels([]string{"low", "medium", "high", "xhigh", "max"}, "high")
		case "claude-opus-5", "claude-sonnet-5", "claude-haiku-5-5":
			values = reasoningSwitch(true)
			values["efforts"], values["default_effort"], values["budget"] = []string{"low", "medium", "high", "xhigh", "max"}, "high", false
			if modelID == "claude-haiku-5-5" {
				values["default_effort"] = "medium"
			}
		case "claude-opus-4-8", "claude-opus-4-7":
			values = reasoningSwitch(false)
			values["efforts"], values["default_effort"], values["budget"] = []string{"low", "medium", "high", "xhigh", "max"}, "high", false
		case "claude-opus-4-6", "claude-sonnet-4-6":
			values = reasoningSwitch(false)
			values["efforts"], values["default_effort"], values["budget"] = []string{"low", "medium", "high", "max"}, "high", true
			values["min_budget"] = 1024
		case "claude-sonnet-4-5", "claude-haiku-4-5":
			values = reasoningSwitch(false)
			values["efforts"], values["budget"], values["min_budget"] = []string{}, true, 1024
		}
		source = "https://platform.claude.com/docs/en/build-with-claude/thinking"
	case "mimo", "mimo-token-plan":
		if protocol != "openai-responses" {
			break
		}
		switch modelID {
		case "mimo-v2.6-flash", "mimo-v2.6-pro", "mimo-v2.6-pro-ultraspeed":
			// All non-none values enable the same behavior. Do not present
			// those aliases as distinct reasoning intensities.
			values = reasoningSwitch(true)
			values["efforts"], values["budget"] = []string{}, false
			values["on_effort"], values["off_effort"] = "high", "none"
			source = "https://mimo.mi.com/docs/en-US/api/chat/responses"
		}
	case "deepseek":
		if protocol != "openai-completions" && protocol != "openai-responses" {
			break
		}
		switch modelID {
		case "deepseek-flash", "deepseek-v4-pro":
			values = reasoningSwitch(true)
			values["efforts"], values["default_effort"], values["budget"] = []string{"low", "high", "max"}, "high", false
			if protocol == "openai-responses" {
				values["on_effort"], values["off_effort"] = "high", "none"
			}
			source = "https://api-docs.deepseek.com/guides/thinking_mode/"
		}
	case "moonshot":
		if protocol == "openai-responses" && modelID == "kimi-k3" {
			values = reasoningLevels([]string{"low", "high", "max"}, "max")
			source = "https://platform.kimi.com/docs/api/responses"
			break
		}
		if protocol != "openai-completions" {
			break
		}
		switch modelID {
		case "kimi-k3":
			values = reasoningMandatory()
			values["efforts"], values["default_effort"], values["budget"] = []string{"low", "high", "max"}, "max", false
		case "kimi-k2.6":
			values = reasoningSwitch(true)
			values["efforts"], values["budget"] = []string{}, false
		case "kimi-k2.7-code", "kimi-k2.7-code-highspeed":
			values = reasoningMandatory()
			values["efforts"], values["budget"] = []string{}, false
		}
		source = "https://platform.kimi.com/docs/guide/use-thinking-models"
	case "kimi-code":
		if protocol != "openai-completions" && protocol != "openai-responses" {
			break
		}
		switch modelID {
		case "k3", "k3-256k":
			values = reasoningLevels([]string{"low", "high", "max"}, "high")
		case "kimi-for-coding":
			values = reasoningLevels([]string{"low", "high", "max"}, "max")
		case "kimi-for-coding-highspeed":
			values = reasoningMandatory()
			values["efforts"], values["budget"] = []string{}, false
		}
		source = "https://www.kimi.com/code/docs/kimi-code/models.html"
	case "zai", "zai-coding-plan", "zhipu", "zhipu-coding-plan":
		if (provider.PresetID == "zhipu" || provider.PresetID == "zhipu-coding-plan") && protocol == "openai-responses" && modelID == "glm-5.3" {
			// This endpoint documents different effective levels and off
			// semantics from Chat. Do not carry either contract across APIs.
			values = reasoningResponseLevels([]string{"high", "max"}, "max")
			source = "https://docs.bigmodel.cn/cn/guide/develop/responses/introduction"
			break
		}
		if protocol != "openai-completions" {
			break
		}
		switch modelID {
		case "glm-5.3", "glm-5.3-flash":
			values = reasoningMandatory()
		case "glm-5.2", "glm-5.1", "glm-5", "glm-4.7":
			values = reasoningSwitch(true)
		}
		if values != nil && (provider.PresetID == "zai" || provider.PresetID == "zai-coding-plan") {
			switch modelID {
			case "glm-5.3", "glm-5.3-flash":
				values["efforts"], values["default_effort"], values["budget"] = []string{"low", "high", "max"}, "max", false
			case "glm-5.2":
				// none/minimal skip thinking; medium and xhigh are aliases.
				values["efforts"], values["default_effort"], values["budget"] = []string{"high", "max"}, "max", false
			}
			source = "https://docs.z.ai/api-reference/llm/chat-completion"
		} else {
			source = "https://docs.bigmodel.cn/cn/guide/capabilities/thinking-mode"
		}
	case "qwen", "bailian-coding-plan":
		if provider.PresetID == "qwen" && protocol == "openai-responses" {
			// The Responses endpoint publishes its own effective levels;
			// Chat enable_thinking/thinking_budget must not leak here.
			switch modelID {
			case "qwen3.8-max", "qwen3.8-max-0902", "qwen3.8-flash", "qwen3.8-2.4t-a95b", "qwen3.8-27b":
				values = reasoningResponseLevels([]string{"low", "medium", "xhigh"}, "xhigh")
			case "deepseek-v4-flash-0731", "deepseek-v4-pro-0813", "deepseek-v4.1-flash":
				values = reasoningResponseLevels([]string{"low", "high", "max"}, "high")
			case "glm-5.2", "deepseek-v4-pro":
				values = reasoningResponseLevels([]string{"high", "max"}, "high")
			case "glm-5.3":
				values = reasoningLevels([]string{"low", "high", "max"}, "max")
			}
			source = "https://www.alibabacloud.com/help/en/model-studio/qwen-api-via-openai-responses"
			break
		}
		if protocol != "openai-completions" {
			break
		}
		switch modelID {
		case "qwen3.8-max", "qwen3.8-flash", "qwen3.8-27b",
			"qwen3.7-max", "qwen3.7-max-2026-05-20", "qwen3.7-max-2026-06-08",
			"qwen3.7-plus", "qwen3.7-plus-2026-05-26", "qwen3.7-flash", "qwen3.7-flash-2026-07-15",
			"qwen3.6-plus", "qwen3.6-flash", "qwen3.5-plus", "qwen3.5-flash":
			values = reasoningSwitch(true)
			values["budget"] = true
		case "qwen-plus", "qwen-plus-latest", "qwen-flash", "kimi-k2.6", "kimi-k2.5":
			values = reasoningSwitch(false)
			values["budget"] = true
		case "qwen3.8-2.4t-a95b", "qwen3.7-max-preview", "qwen3.7-max-2026-05-17", "glm-5.3", "kimi-k2.7-code":
			values = reasoningMandatory()
			values["budget"] = true
		case "glm-5.2", "glm-5.2-fast-preview", "glm-5.1", "glm-5":
			values = reasoningSwitch(true)
			values["budget"] = true
		case "deepseek-v4.1-flash", "deepseek-v4-pro":
			values = reasoningSwitch(true)
			values["budget"] = false
		case "MiniMax-M2.5":
			values = reasoningMandatory()
			values["budget"] = false
		}
		source = "https://www.alibabacloud.com/help/en/model-studio/deep-thinking"
	case "google":
		if protocol != "google-generative-ai" {
			break
		}
		switch modelID {
		case "gemini-4-argon", "gemini-3.8-flash":
			values = reasoningLevels([]string{"low", "medium", "high"}, "medium")
		case "gemini-3.6-flash":
			values = reasoningLevels([]string{"minimal", "low", "medium", "high"}, "medium")
		case "gemini-3.5-flash-lite", "gemini-3.1-flash-lite":
			values = reasoningLevels([]string{"minimal", "low", "medium", "high"}, "minimal")
		case "gemini-3.1-flash-lite-image":
			values = reasoningLevels([]string{"minimal", "high"}, "minimal")
		case "gemini-3-flash-preview":
			values = reasoningLevels([]string{"minimal", "low", "medium", "high"}, "high")
		case "gemini-2.5-pro":
			values = reasoningMandatory()
			values["budget"], values["dynamic_budget"], values["efforts"] = true, true, []string{}
			values["min_budget"], values["max_budget"] = 128, 32768
		case "gemini-2.5-flash":
			values = reasoningSwitch(true)
			values["budget"], values["dynamic_budget"], values["efforts"] = true, true, []string{}
			values["min_budget"], values["max_budget"] = 0, 24576
		case "gemini-2.5-flash-lite":
			values = reasoningSwitch(false)
			values["budget"], values["dynamic_budget"], values["efforts"] = true, true, []string{}
			values["min_budget"], values["max_budget"] = 512, 24576
		}
		source = "https://ai.google.dev/gemini-api/docs/generate-content/thinking"
	case "minimax", "minimax-token-plan":
		if protocol != "anthropic-messages" {
			break
		}
		switch modelID {
		case "MiniMax-M3":
			values = reasoningSwitch(false)
			values["efforts"], values["budget"] = []string{}, false
		case "MiniMax-M3.1-Flash":
			values = reasoningLevels([]string{"low", "medium", "high", "xhigh", "max"}, "max")
		}
		source = "https://platform.minimax.io/docs/api-reference/text-anthropic-api"
	case "xai":
		if protocol != "openai-responses" && protocol != "openai-completions" {
			break
		}
		switch modelID {
		case "grok-4.7", "grok-4.6":
			values = reasoningLevels([]string{"low", "medium", "high", "xhigh"}, "high")
		case "grok-4.5":
			values = reasoningLevels([]string{"low", "medium", "high"}, "high")
		}
		source = "https://docs.x.ai/developers/model-capabilities/text/reasoning"
	}
	if len(values) == 0 {
		return nil, ""
	}
	result := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		result[key] = raw(value)
	}
	return result, source
}

func reasoningSwitch(defaultOn bool) map[string]any {
	return map[string]any{"supported": true, "toggle": true, "disable": true, "default_on": defaultOn}
}

func reasoningMandatory() map[string]any {
	return map[string]any{"supported": true, "toggle": false, "disable": false, "default_on": true}
}

func reasoningLevels(efforts []string, defaultEffort string) map[string]any {
	values := reasoningMandatory()
	values["efforts"], values["default_effort"], values["budget"] = efforts, defaultEffort, false
	return values
}

func reasoningResponseLevels(efforts []string, defaultEffort string) map[string]any {
	values := reasoningLevels(efforts, defaultEffort)
	values["toggle"], values["disable"] = true, true
	values["on_effort"], values["off_effort"] = defaultEffort, "none"
	return values
}

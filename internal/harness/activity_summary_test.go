package harness

import (
	"context"
	"encoding/json"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral/internal/domain"
)

func TestActivitySummaryReasoningOnProviderWire(t *testing.T) {
	enabled := true
	budget := 1024
	tests := []struct {
		name, protocol, dialect string
		caps                    models.ReasoningCapabilities
		reasoning               *core.ReasoningOptions
		profileLimit, want      int
		wantEffort, wantBudget  any
	}{
		{name: "optional budget omitted", protocol: "anthropic-messages", dialect: "anthropic",
			caps:         models.ReasoningCapabilities{Supported: &enabled, Budget: &enabled, MinBudget: &budget},
			profileLimit: 4096, want: 40},
		{name: "forced budget", protocol: "anthropic-messages", dialect: "anthropic",
			caps:         models.ReasoningCapabilities{Supported: &enabled, DefaultOn: &enabled, Budget: &enabled, MinBudget: &budget},
			profileLimit: 4096, want: 1064, wantBudget: float64(1024)},
		{name: "explicit budget", protocol: "anthropic-messages", dialect: "anthropic",
			caps:      models.ReasoningCapabilities{Supported: &enabled, Budget: &enabled, MinBudget: &budget},
			reasoning: &core.ReasoningOptions{BudgetTokens: &budget}, profileLimit: 4096, want: 1064, wantBudget: float64(1024)},
		{name: "lowest active effort", protocol: "openai-completions", dialect: "openai",
			caps:         models.ReasoningCapabilities{Supported: &enabled, Efforts: []string{"low", "minimal"}},
			profileLimit: 1024, want: 512, wantEffort: "minimal"},
		{name: "explicit effort capped to the profile", protocol: "openai-completions", dialect: "openai",
			caps:      models.ReasoningCapabilities{Supported: &enabled, Efforts: []string{"minimal", "low"}},
			reasoning: &core.ReasoningOptions{Effort: "low"}, profileLimit: 256, want: 256, wantEffort: "low"},
		{name: "off retains short output", protocol: "openai-completions", dialect: "openai",
			caps:         models.ReasoningCapabilities{Supported: &enabled, OffEffort: "none", Efforts: []string{"low"}},
			profileLimit: 1024, want: 40, wantEffort: "none"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan map[string]any, 1)
			f := newFixture(t, 0, func(_ context.Context, _ int, body map[string]any) any {
				requests <- body
				if test.protocol == "anthropic-messages" {
					return map[string]any{"id": "summary", "type": "message", "role": "assistant", "model": "summary", "stop_reason": "end_turn",
						"content": []map[string]string{{"type": "text", "text": "Checking results"}}}
				}
				return textReply("Checking results")
			})
			var primary domain.ModelProfile
			if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &primary); err != nil {
				t.Fatal(err)
			}
			var original domain.Provider
			if err := f.store.Get(f.ctx, "providers", primary.ProviderID, &original); err != nil {
				t.Fatal(err)
			}
			provider := domain.Provider{Name: "summary", Protocol: test.protocol, Dialect: test.dialect, BaseURL: original.BaseURL}
			if err := f.service.Models.SaveProvider(f.ctx, &provider, nil, nil); err != nil {
				t.Fatal(err)
			}
			profile := domain.ModelProfile{Name: "summary", ProviderID: provider.ID, ModelID: "summary", Recommendations: map[string]json.RawMessage{
				"context_window": encode(128000), "max_output_tokens": encode(test.profileLimit), "streaming": encode(false), "reasoning": encode(test.caps),
			}}
			if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
				t.Fatal(err)
			}
			f.agent.ActivitySummaryMode, f.agent.ActivitySummaryModelID, f.agent.ActivitySummaryReasoning = "model", profile.ID, test.reasoning
			if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
				t.Fatal(err)
			}
			r, err := f.service.open(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			o := &activityObserver{runtime: r}
			summary, pause, err := o.callSummary(f.ctx, activityCandidate{userGoal: "Check progress"}, "")
			if err != nil || pause || summary != "Checking results" {
				t.Fatalf("summary = %q, pause = %t, err = %v", summary, pause, err)
			}
			select {
			case body := <-requests:
				tokens := body["max_tokens"]
				if tokens == nil {
					tokens = body["max_completion_tokens"]
				}
				if tokens != float64(test.want) || body["reasoning_effort"] != test.wantEffort {
					t.Fatalf("max output = %v, effort = %v, want %d and %v", tokens, body["reasoning_effort"], test.want, test.wantEffort)
				}
				thinking, _ := body["thinking"].(map[string]any)
				if thinking["budget_tokens"] != test.wantBudget || test.wantBudget == nil && body["thinking"] != nil {
					t.Fatalf("thinking = %v, want budget %v", body["thinking"], test.wantBudget)
				}
			default:
				t.Fatal("summary returned without a provider request")
			}
		})
	}
}

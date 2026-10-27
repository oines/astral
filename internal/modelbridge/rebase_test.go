package modelbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral-core/models/integration"
	"github.com/oines/astral/internal/domain"
)

func sourcedReasoningProfile(connection domain.Provider, id string) domain.ModelProfile {
	at := time.Now().UTC()
	profile := reasoningIntegrationProfile(connection, id, map[string]any{"supported": true, "toggle": true, "disable": true, "efforts": []string{"high"}})
	profile.RecommendationSources = map[string]domain.RecommendationSource{"context_window": {Source: "public/model", UpdatedAt: at}, "reasoning.supported": {Source: "old-route/models", UpdatedAt: at}, "reasoning.toggle": {Source: "old-route/models", UpdatedAt: at}, "reasoning.efforts": {Source: "old-route/models", UpdatedAt: at}, "streaming": {Source: "old-route/models", UpdatedAt: at}}
	return profile
}
func TestProviderProtocolChangeRebasesInheritedModelsInOneTransaction(t *testing.T) {
	ctx := context.Background()
	bridge := testBridge(t)
	bridge.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("configuration change contacted upstream")
		return nil, nil
	})}
	connection := domain.Provider{Name: "route", Protocol: "openai-completions", Dialect: "compatible", BaseURL: "https://fixture.example/v1"}
	if err := bridge.SaveProvider(ctx, &connection, nil, nil); err != nil {
		t.Fatal(err)
	}
	inherited := sourcedReasoningProfile(connection, "inherited")
	inherited.Overrides["reasoning"] = raw(map[string]any{"supported": true, "efforts": []string{"user-native"}})
	inherited.Overrides["max_output_tokens"] = raw(2048)
	if err := bridge.SaveProfile(ctx, &inherited); err != nil {
		t.Fatal(err)
	}
	explicit := sourcedReasoningProfile(connection, "explicit")
	explicit.Protocol = "openai-completions"
	if err := bridge.SaveProfile(ctx, &explicit); err != nil {
		t.Fatal(err)
	}
	overrideBefore, _ := json.Marshal(inherited.Overrides)
	explicitBefore, _ := json.Marshal(explicit)
	connection.Protocol = "openai-responses"
	if err := bridge.SaveProvider(ctx, &connection, nil, nil); err != nil {
		t.Fatal(err)
	}
	var saved domain.ModelProfile
	if err := bridge.Store.Get(ctx, "models", inherited.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision != inherited.Revision+1 || len(saved.Recommendations["reasoning"]) != 0 || len(saved.Recommendations["streaming"]) != 0 || string(saved.Recommendations["context_window"]) != "128000" {
		t.Fatalf("old inherited route metadata survived: %+v", saved)
	}
	for field := range saved.RecommendationSources {
		if field == "streaming" || len(field) >= 10 && field[:10] == "reasoning." {
			t.Fatalf("stale source survived rebase: %s", field)
		}
	}
	overrideAfter, _ := json.Marshal(saved.Overrides)
	if string(overrideBefore) != string(overrideAfter) || !saved.UserModified {
		t.Fatal("route change modified user overrides or inventory protection")
	}
	described := bridge.DescribeProfile(ctx, saved)
	if described.ReasoningError != "" || described.ReasoningControls == nil || described.ReasoningControls.Format != "openai-responses" {
		t.Fatalf("new route not effective: %+v", described)
	}
	if err := bridge.Store.Get(ctx, "models", explicit.ID, &explicit); err != nil {
		t.Fatal(err)
	}
	explicitAfter, _ := json.Marshal(explicit)
	if string(explicitBefore) != string(explicitAfter) {
		t.Fatal("explicit model route was changed by provider default")
	}
	// A later failure while reading child configuration rolls back the provider
	// update and every rebased child; no partially changed route is published.
	before := currentInventory(t, bridge)
	beforeProvider := connection
	corruptMeta := domain.Meta{ID: domain.ID("corrupt")}
	if err := bridge.Store.Save(ctx, "models", &corruptMeta, map[string]any{"provider_id": connection.ID, "protocol": []string{"invalid"}}); err != nil {
		t.Fatal(err)
	}
	connection.Protocol = "openai-completions"
	if err := bridge.SaveProvider(ctx, &connection, nil, nil); err == nil {
		t.Fatal("invalid child accepted partial transaction")
	}
	var actualProvider domain.Provider
	if err := bridge.Store.Get(ctx, "providers", connection.ID, &actualProvider); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualProvider, beforeProvider) {
		t.Fatalf("provider committed before child failure: %+v", actualProvider)
	}
	if err := bridge.DeleteProfile(ctx, corruptMeta.ID, corruptMeta.Revision); err != nil {
		t.Fatal(err)
	}
	requireInventoryUnchanged(t, bridge, before)
}
func TestModelProtocolChangeDropsOldRecommendationSourcesAndPreservesOverrides(t *testing.T) {
	ctx := context.Background()
	bridge := testBridge(t)
	connection := domain.Provider{Name: "route", Protocol: "openai-completions", Dialect: "compatible", BaseURL: "https://fixture.example/v1"}
	if err := bridge.SaveProvider(ctx, &connection, nil, nil); err != nil {
		t.Fatal(err)
	}
	profile := sourcedReasoningProfile(connection, "manual")
	profile.Overrides["reasoning"] = raw(map[string]any{"efforts": []string{"custom-level"}})
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Protocol = "openai-responses"
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	var stored domain.ModelProfile
	if err := bridge.Store.Get(ctx, "models", profile.ID, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Recommendations["reasoning"]) != 0 || len(stored.RecommendationSources) != 1 || stored.RecommendationSources["context_window"].Source != "public/model" || string(stored.Overrides["reasoning"]) != `{"efforts":["custom-level"]}` {
		t.Fatalf("model rebase lost provenance boundary: %+v", stored)
	}
}

func TestNewCatalogBindingSurvivesModelAndProtocolEdit(t *testing.T) {
	for _, preset := range []string{"", "commandcode"} {
		t.Run("preset="+preset, func(t *testing.T) {
			ctx := context.Background()
			bridge := testBridge(t)
			bridge.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("editing a profile contacted upstream")
				return nil, nil
			})}
			connection := domain.Provider{Name: "gateway", PresetID: preset, Protocol: "openai-completions", Dialect: "compatible", BaseURL: "https://fixture.example/v1"}
			if err := bridge.SaveProvider(ctx, &connection, nil, nil); err != nil {
				t.Fatal(err)
			}
			profile := reasoningIntegrationProfile(connection, "vendor/old", map[string]any{"supported": true, "efforts": []string{"high"}})
			if err := bridge.SaveProfile(ctx, &profile); err != nil {
				t.Fatal(err)
			}
			profile.ModelID, profile.Protocol, profile.Dialect = "vendor/new", "openai-responses", "openai"
			// The new catalog may publish exactly the same values. Its binding,
			// not a map comparison with the old model, establishes freshness.
			target, err := integration.RecommendationTargetFor(sdkProvider(connection), sdkModel(profile))
			if err != nil {
				t.Fatal(err)
			}
			profile.RecommendationTarget = &target
			profile.RecommendationSources = map[string]domain.RecommendationSource{"reasoning.efforts": {Source: "new-route/models"}}
			preview := bridge.PreviewProfile(ctx, profile)
			if preview.ReasoningError != "" || preview.ReasoningControls == nil || len(preview.Recommendations["reasoning"]) == 0 || preview.RecommendationSources["reasoning.efforts"].Source != "new-route/models" {
				t.Fatalf("preview discarded new catalog facts: %+v", preview)
			}
			profile = preview
			profile.Overrides["defaults"] = raw(core.GenerationOptions{Reasoning: core.ReasoningOptions{Effort: "high"}})
			if err := bridge.SaveProfile(ctx, &profile); err != nil {
				t.Fatal(err)
			}
			resolved, err := bridge.ResolveSelection(ctx, core.ModelSelection{ID: profile.ID})
			if err != nil {
				t.Fatal(err)
			}
			request := core.Request{ModelID: resolved.Model.Spec().ID, ModelRevision: resolved.Model.Spec().Revision,
				Messages: []core.Message{core.User("offline validation")}, MaxOutputTokens: 1024, Options: resolved.Options}
			if err := core.ValidateRequest(ctx, resolved.Model, request); err != nil {
				t.Fatal(err)
			}
			encoder := resolved.Model.(*boundModel).adapter.(interface {
				RequestBody(core.Request) (map[string]any, error)
			})
			body, err := encoder.RequestBody(request)
			if err != nil || body["model"] != "vendor/new" || !reflect.DeepEqual(body["reasoning"], map[string]any{"effort": "high"}) {
				t.Fatalf("saved UI selection did not reach the new route: body=%v error=%v", body, err)
			}
			// An explicit old default is retained for review, then rejected by
			// the new route rather than silently changed to another effort.
			revision := profile.Revision
			profile.Overrides["defaults"] = raw(core.GenerationOptions{Reasoning: core.ReasoningOptions{Effort: "not-listed"}})
			if err := bridge.SaveProfile(ctx, &profile); err == nil {
				t.Fatal("an unsupported explicit default was silently accepted")
			}
			if string(profile.Overrides["defaults"]) != string(raw(core.GenerationOptions{Reasoning: core.ReasoningOptions{Effort: "not-listed"}})) {
				t.Fatal("rejected configuration lost the user's explicit default")
			}
			var stored domain.ModelProfile
			if err := bridge.Store.Get(ctx, "models", profile.ID, &stored); err != nil || stored.Revision != revision {
				t.Fatalf("rejected defaults changed saved configuration: %+v, %v", stored, err)
			}
		})
	}
}

func TestChangingModelIDCannotRebindOldCatalogFacts(t *testing.T) {
	ctx := context.Background()
	bridge := testBridge(t)
	connection := domain.Provider{Name: "custom", Protocol: "openai-responses", Dialect: "openai", BaseURL: "https://fixture.example/v1"}
	if err := bridge.SaveProvider(ctx, &connection, nil, nil); err != nil {
		t.Fatal(err)
	}
	profile := sourcedReasoningProfile(connection, "vendor/old")
	profile.Overrides["reasoning"] = raw(map[string]any{"supported": true, "efforts": []string{"user-level"}})
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(profile)
	profile.ModelID = "vendor/new"
	preview := bridge.PreviewProfile(ctx, profile)
	if len(preview.Recommendations) != 0 || len(preview.RecommendationSources) != 0 || preview.RecommendationTarget.ModelID != "vendor/new" {
		t.Fatalf("old catalog facts silently became new facts: %+v", preview)
	}
	if preview.ReasoningControls == nil || preview.ReasoningError != "" || preview.UnavailableReason != "token_limits_unknown" || len(preview.Overrides["reasoning"]) == 0 {
		t.Fatalf("rebase lost explicit controls or budget separation: %+v", preview)
	}
	if err := bridge.SaveProfile(ctx, &profile); err == nil {
		t.Fatal("saving a new model inherited the old model's limits")
	}
	var saved domain.ModelProfile
	if err := bridge.Store.Get(ctx, "models", profile.ID, &saved); err != nil {
		t.Fatal(err)
	}
	actual, _ := json.Marshal(saved)
	if string(actual) != string(original) {
		t.Fatal("rejected model edit changed saved configuration")
	}
}

func TestProviderAPIBindingChangesInvalidateOnlyOldAPIFacts(t *testing.T) {
	for _, change := range []string{"endpoint", "format", "mapping"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			bridge := testBridge(t)
			connection := domain.Provider{Name: "custom", Protocol: "openai-completions", Dialect: "compatible", BaseURL: "https://fixture.example/old"}
			if change == "mapping" {
				connection.ReasoningFormat = models.ReasoningFormatCustom
				connection.ReasoningMapping = &models.ReasoningMapping{On: raw(map[string]any{"thinking": true})}
			}
			if err := bridge.SaveProvider(ctx, &connection, nil, nil); err != nil {
				t.Fatal(err)
			}
			profile := sourcedReasoningProfile(connection, "vendor/model")
			profile.Recommendations["reasoning_format"] = raw(models.ReasoningFormatOpenAIChat)
			profile.Recommendations["input_price"] = raw(1)
			profile.Overrides["max_output_tokens"] = raw(2048)
			if err := bridge.SaveProfile(ctx, &profile); err != nil {
				t.Fatal(err)
			}
			contract := profile.RecommendationTarget.Contract
			switch change {
			case "endpoint":
				connection.BaseURL = "https://fixture.example/new"
			case "format":
				connection.ReasoningFormat = models.ReasoningFormatThinkingBoolean
			case "mapping":
				connection.ReasoningMapping = &models.ReasoningMapping{On: raw(map[string]any{"thinking": map[string]any{"type": "enabled"}})}
			}
			if err := bridge.SaveProvider(ctx, &connection, nil, nil); err != nil {
				t.Fatal(err)
			}
			var saved domain.ModelProfile
			if err := bridge.Store.Get(ctx, "models", profile.ID, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.RecommendationTarget.Contract == contract || len(saved.Recommendations["reasoning"]) > 0 || len(saved.Recommendations["reasoning_format"]) > 0 || len(saved.Recommendations["streaming"]) > 0 {
				t.Fatalf("old API facts survived %s: %+v", change, saved)
			}
			if string(saved.Recommendations["context_window"]) != "128000" || string(saved.Recommendations["input_price"]) != "1" || string(saved.Overrides["max_output_tokens"]) != "2048" || !saved.UserModified {
				t.Fatalf("API change erased model facts or explicit configuration: %+v", saved)
			}
			if len(saved.RecommendationSources) != 1 || saved.RecommendationSources["context_window"].Source != "public/model" {
				t.Fatalf("API provenance did not follow filtering: %+v", saved.RecommendationSources)
			}
		})
	}
}

func TestChangingProfileProviderIDDoesNotCarryTheOldConnectionContract(t *testing.T) {
	ctx := context.Background()
	bridge := testBridge(t)
	old := domain.Provider{Name: "first", Protocol: "openai-responses", Dialect: "openai", BaseURL: "https://fixture.example/v1"}
	next := old
	next.Name = "second"
	for _, connection := range []*domain.Provider{&old, &next} {
		if err := bridge.SaveProvider(ctx, connection, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	profile := sourcedReasoningProfile(old, "vendor/model")
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	contract := profile.RecommendationTarget.Contract
	profile.ProviderID = next.ID
	preview := bridge.PreviewProfile(ctx, profile)
	if preview.RecommendationTarget.Contract == contract || len(preview.Recommendations["reasoning"]) != 0 || len(preview.Recommendations["context_window"]) == 0 {
		t.Fatalf("preview reused another connection's API contract: %+v", preview)
	}
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.RecommendationTarget.Contract != preview.RecommendationTarget.Contract || len(profile.Recommendations["reasoning"]) != 0 {
		t.Fatal("save and preview used different connection boundaries")
	}
}

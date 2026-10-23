package modelbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func testBridge(t *testing.T) *Bridge {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Bridge{Store: store}
}

func TestProviderPresetsConstructNativeAdapters(t *testing.T) {
	b := testBridge(t)
	ctx := context.Background()
	seen := map[string]bool{}
	for _, preset := range ProviderPresets() {
		if seen[preset.ID] {
			t.Fatalf("duplicate preset %s", preset.ID)
		}
		seen[preset.ID] = true
		provider := domain.Provider{Meta: domain.Meta{ID: preset.ID}, PresetID: preset.ID, Name: preset.Name, Protocol: preset.Protocol, Dialect: preset.Dialect, BaseURL: preset.BaseURL}
		provider.BaseURL = strings.ReplaceAll(strings.ReplaceAll(provider.BaseURL, "<resource>", "example"), "<account_id>", "example")
		if provider.BaseURL == "" {
			provider.BaseURL = "https://example.com/v1"
		}
		profile := domain.ModelProfile{ModelID: "test-model", ProviderID: preset.ID, Name: "Test model", Recommendations: map[string]json.RawMessage{"context_window": raw(128000), "max_output_tokens": raw(4096)}}
		if _, err := b.construct(ctx, profile, provider); err != nil {
			t.Errorf("%s: %v", preset.ID, err)
		}
	}
	for _, id := range []string{"commandcode", "opencode", "opencode-go", "mimo", "mimo-token-plan", "minimax-token-plan", "zai-coding-plan", "zhipu-coding-plan", "bailian-coding-plan", "kimi-code"} {
		if !seen[id] {
			t.Errorf("missing requested provider %s", id)
		}
	}
}

func TestModelRoutesAndSessionHeaders(t *testing.T) {
	for _, tc := range []struct{ protocol, suffix, dialect string }{
		{"anthropic-messages", "/anthropic/v1", "anthropic"},
		{"google-generative-ai", "/google/v1beta", "google"},
		{"openai-responses", "/openai/v1", "openai"},
		{"openai-completions", "/openai/v1", "compatible"},
	} {
		p := FindPreset("opencode-go")
		protocol, dialect, base, err := modelRoute(domain.ModelProfile{Protocol: tc.protocol}, domain.Provider{PresetID: p.ID, Protocol: p.Protocol, Dialect: p.Dialect, BaseURL: p.BaseURL})
		if err != nil || protocol != tc.protocol || dialect != tc.dialect || base != p.BaseURL+tc.suffix {
			t.Fatalf("route %s: %s %s %s %v", tc.protocol, protocol, dialect, base, err)
		}
	}
	if sessionHeaders("opencode-go", "s1")["X-Opencode-Session"] != "s1" || sessionHeaders("github-copilot", "s2")["X-Interaction-Id"] != "s2" || sessionHeaders("openai", "s3") != nil {
		t.Fatal("session metadata escaped its provider")
	}
}

func TestCatalogImportKeepsEditsAndSkipsUnknownLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"known","context_window":128000,"max_output_tokens":16000,"supported_endpoints":["/chat/completions","/responses"]},{"id":"unknown"},{"id":"missing-output","context_window":64000}]}`))
	}))
	defer server.Close()
	b := testBridge(t)
	ctx := context.Background()
	provider := domain.Provider{Name: "fixture", Protocol: "openai-completions", Dialect: "compatible", BaseURL: server.URL}
	if err := b.SaveProvider(ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	first, err := b.ImportModels(ctx, provider.ID, ImportModelsInput{Refresh: true})
	if err != nil || len(first.Imported) != 1 || len(first.Skipped) != 2 || first.Skipped[0] != "unknown" || first.Skipped[1] != "missing-output" {
		t.Fatalf("first import: %+v %v", first, err)
	}
	profile := first.Imported[0]
	if profile.Protocol != "openai-responses" || profile.Verification != nil {
		t.Fatal("preferred protocol or unverified status lost")
	}
	profile.Name = "My model"
	profile.Description = "Keep this description"
	profile.Overrides["max_output_tokens"] = raw(1024)
	if err := b.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	second, err := b.ImportModels(ctx, provider.ID, ImportModelsInput{Refresh: true})
	if err != nil || len(second.Imported) != 0 || len(second.Skipped) != 3 {
		t.Fatalf("second import: %+v %v", second, err)
	}
	var saved domain.ModelProfile
	if err := b.Store.Get(ctx, "models", profile.ID, &saved); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(profile)
	after, _ := json.Marshal(saved)
	if string(before) != string(after) {
		t.Fatal("catalog import changed an existing profile")
	}
}

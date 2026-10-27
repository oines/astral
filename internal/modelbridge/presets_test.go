package modelbridge

import (
	"context"
	"encoding/json"
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

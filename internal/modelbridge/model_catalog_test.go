package modelbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
)

const catalogFixture = `{
 "openai":{"models":{"gpt-fixture":{"name":"Original model","limit":{"context":128000,"output":16000},"tool_call":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}],"cost":{"input":1},"interleaved":{"field":"reasoning_content"}}}},
 "openrouter":{"models":{"openai/gpt-fixture":{"limit":{"context":64000},"tool_call":false,"reasoning_options":[],"cost":{"input":2}}}},
 "other":{"models":{"openai/gpt-fixture":{"limit":{"context":32000},"reasoning":true,"reasoning_options":[{"type":"effort","values":["medium"]}]}}}
}`

func fixtureSnapshot(t *testing.T, fixture string) *catalogSnapshot {
	t.Helper()
	snapshot, err := parseModelCatalog([]byte(fixture), time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func checkRecommendation(t *testing.T, item Discovery, field, expected string) {
	t.Helper()
	got := flattenRecommendations(item.Recommendations)[field]
	if !sameJSON(got, json.RawMessage(expected)) {
		t.Fatalf("%s: got %s, want %s; all=%v", field, got, expected, item.Recommendations)
	}
}

func TestRuntimeCatalogMatchesExactSlugAndPreservesFullIdentity(t *testing.T) {
	snapshot := fixtureSnapshot(t, catalogFixture)
	provider := domain.Provider{Protocol: "openai-completions", Dialect: "compatible"}
	for _, id := range []string{"gpt-fixture", "vendor/gpt-fixture", "gateway/vendor/gpt-fixture"} {
		item := snapshot.recommend(provider, id)
		if item.ID != id {
			t.Fatalf("model identity changed: %s -> %s", id, item.ID)
		}
		checkRecommendation(t, item, "context_window", "128000")
		checkRecommendation(t, item, "reasoning.efforts", `["low","high"]`)
		if item.RecommendationSources["context_window"].Source != modelsDevURL+"#openai/gpt-fixture" {
			t.Fatal("original source was not recorded")
		}
		if _, ok := item.Recommendations["tool_replay"]; ok {
			t.Fatal("catalog identity inferred replay support")
		}
		for _, field := range []string{"reasoning_format", "reasoning_mapping", "defaults"} {
			if _, ok := item.Recommendations[field]; ok {
				t.Fatalf("catalog inferred request setting %s", field)
			}
		}
	}
	for _, id := range []string{"GPT-fixture", "gpt-fixture:free", "gpt-fixture-20261023", "gpt-fixture-v2", "gpt-fixture (fast)"} {
		if item := snapshot.recommend(provider, id); len(item.Recommendations) != 0 {
			t.Fatalf("non-exact slug was matched: %s %+v", id, item)
		}
	}
}

func TestRuntimeCatalogUsesFieldwiseSourcePriority(t *testing.T) {
	snapshot := fixtureSnapshot(t, catalogFixture)
	provider := domain.Provider{PresetID: "openrouter", Protocol: "openai-responses", Dialect: "openai"}
	item := snapshot.recommend(provider, "openai/gpt-fixture")
	checkRecommendation(t, item, "context_window", "64000")
	checkRecommendation(t, item, "max_output_tokens", "16000")
	checkRecommendation(t, item, "tool_calling", `"unsupported"`)
	checkRecommendation(t, item, "reasoning.efforts", `[]`)
	checkRecommendation(t, item, "reasoning.toggle", `false`)
	if item.RecommendationSources["max_output_tokens"].Source != modelsDevURL+"#openai/gpt-fixture" || item.RecommendationSources["context_window"].Source != modelsDevURL+"#openrouter/openai/gpt-fixture" {
		t.Fatal("field provenance does not reflect source priority")
	}
	// Mutating one returned recommendation cannot change the cached snapshot.
	item.Recommendations["context_window"][0] = '9'
	again := snapshot.recommend(provider, "openai/gpt-fixture")
	checkRecommendation(t, again, "context_window", "64000")
}

func TestRuntimeCatalogConflictsStayUnknown(t *testing.T) {
	snapshot := fixtureSnapshot(t, `{
 "one":{"models":{"x":{"limit":{"context":1000,"output":100},"reasoning":null}}},
 "two":{"models":{"x":{"limit":{"context":2000,"output":100},"reasoning":true,"reasoning_options":[{"type":"effort","values":["high"]}]}}}
 }`)
	item := snapshot.recommend(domain.Provider{}, "vendor/x")
	if _, ok := item.Recommendations["context_window"]; ok {
		t.Fatal("conflicting context limits were guessed")
	}
	if _, ok := item.Recommendations["reasoning"]; ok {
		t.Fatal("null/object reasoning conflict was merged")
	}
	checkRecommendation(t, item, "max_output_tokens", "100")
	// Ambiguous same-provider aliases must not silently fall through to a
	// maker's different limit. An exact full ID can still resolve the ambiguity.
	snapshot = fixtureSnapshot(t, `{
 "openai":{"models":{"gpt-x":{"limit":{"context":3000,"output":100}}}},
 "openrouter":{"models":{"a/gpt-x":{"limit":{"context":1000}},"b/gpt-x":{"limit":{"context":2000}}}}
 }`)
	provider := domain.Provider{PresetID: "openrouter"}
	item = snapshot.recommend(provider, "proxy/gpt-x")
	if _, ok := item.Recommendations["context_window"]; ok {
		t.Fatal("ambiguous gateway aliases inherited an unconfirmed maker limit")
	}
	checkRecommendation(t, item, "max_output_tokens", "100")
	checkRecommendation(t, snapshot.recommend(provider, "a/gpt-x"), "context_window", "1000")
}

func TestCatalogMergePreservesFalseEmptyAndNull(t *testing.T) {
	fallback := fixtureSnapshot(t, catalogFixture).recommend(domain.Provider{}, "gpt-fixture")
	live := Discovery{ID: "gpt-fixture", Source: "https://gateway.example/models", Recommendations: map[string]json.RawMessage{
		"context_window": raw(nil), "images": raw("unsupported"),
		"reasoning": raw(map[string]any{"toggle": false, "efforts": []string{}, "default_effort": nil}),
	}}
	markRecommendationSources(&live, time.Now())
	merged := mergeCatalogDiscovery(domain.Provider{}, fallback, live)
	checkRecommendation(t, merged, "context_window", "null")
	checkRecommendation(t, merged, "reasoning.toggle", "false")
	checkRecommendation(t, merged, "reasoning.efforts", "[]")
	checkRecommendation(t, merged, "reasoning.default_effort", "null")
	if merged.RecommendationSources["reasoning.efforts"].Source != live.Source {
		t.Fatal("live source lost")
	}
	live.Recommendations["reasoning"] = raw(map[string]any{"supported": false})
	markRecommendationSources(&live, time.Now())
	merged = mergeCatalogDiscovery(domain.Provider{}, fallback, live)
	if string(merged.Recommendations["reasoning"]) != `{"supported":false}` {
		t.Fatalf("unsupported live reasoning inherited controls: %s", merged.Recommendations["reasoning"])
	}
	live.Recommendations["reasoning"] = raw(nil)
	merged = mergeCatalogDiscovery(domain.Provider{}, fallback, live)
	checkRecommendation(t, merged, "reasoning", "null")
}

func TestCatalogCacheRefreshKeepsLastValidSnapshot(t *testing.T) {
	var requests atomic.Int32
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Error("public catalog received a credential or non-GET request")
		}
		switch mode.Load() {
		case 1:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case 2:
			_, _ = w.Write([]byte(`{"invalid":true}`))
		default:
			_, _ = w.Write([]byte(catalogFixture))
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "cache", "models.dev.json")
	now := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	catalog := NewModelCatalog(path, server.Client())
	catalog.url, catalog.now = server.URL, func() time.Time { return now }
	if requests.Load() != 0 {
		t.Fatal("constructor performed I/O")
	}
	first, err := catalog.load(context.Background())
	if err != nil || first == nil || requests.Load() != 1 {
		t.Fatalf("initial load: %v %v", first, err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A new process starts from the persisted, fresh cache without a request.
	restarted := NewModelCatalog(path, server.Client())
	restarted.url, restarted.now = server.URL, catalog.now
	if _, err := restarted.load(context.Background()); err != nil || requests.Load() != 1 {
		t.Fatalf("fresh disk cache requested the network: %v", err)
	}
	now = now.Add(25 * time.Hour)
	mode.Store(1)
	stale, err := restarted.load(context.Background())
	if err == nil || stale == nil || !stale.FetchedAt.Equal(first.FetchedAt) {
		t.Fatal("failed refresh did not preserve last valid snapshot")
	}
	_, _ = restarted.load(context.Background())
	if requests.Load() != 2 {
		t.Fatal("failed public refresh was immediately repeated")
	}
	mode.Store(2)
	now = now.Add(time.Minute)
	if stale, err = restarted.load(context.Background()); err == nil || stale == nil {
		t.Fatal("invalid catalog discarded stale data")
	}
	if data, _ := os.ReadFile(path); string(data) != string(saved) {
		t.Fatal("failed refresh replaced the valid disk cache")
	}
	mode.Store(0)
	now = now.Add(time.Minute)
	fresh, err := restarted.load(context.Background())
	if err != nil || !fresh.FetchedAt.Equal(now) {
		t.Fatal("valid refresh did not replace stale data")
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".models-dev-*"))
	if len(files) != 0 {
		t.Fatal("atomic cache writer leaked temporary files")
	}
}

func TestCatalogCancellationDoesNotThrottleNextLookup(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(catalogFixture))
	}))
	defer server.Close()
	catalog := NewModelCatalog("", server.Client())
	catalog.url = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := catalog.load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if _, err := catalog.load(context.Background()); err != nil || requests.Load() != 1 {
		t.Fatalf("cancellation throttled a later request: %v", err)
	}
}

func TestCatalogConcurrentLookupsShareOneFetch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(catalogFixture))
	}))
	defer server.Close()
	catalog := NewModelCatalog("", server.Client())
	catalog.url = server.URL
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			snapshot, err := catalog.load(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			item := snapshot.recommend(domain.Provider{}, "gpt-fixture")
			checkRecommendation(t, item, "context_window", "128000")
			item.Recommendations["context_window"][0] = '9'
		}()
	}
	close(start)
	workers.Wait()
	if requests.Load() != 1 {
		t.Fatalf("concurrent lookups made %d public requests", requests.Load())
	}
}

func TestCatalogKeepsValidatedResponseWhenDiskCacheCannotBeWritten(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(catalogFixture))
	}))
	defer server.Close()
	// Renaming a cache file onto an existing directory must fail, while the
	// successfully parsed public response can still serve this process.
	catalog := NewModelCatalog(t.TempDir(), server.Client())
	catalog.url = server.URL
	snapshot, err := catalog.load(context.Background())
	if err == nil || snapshot == nil {
		t.Fatalf("valid response was discarded on disk failure: %v %v", snapshot, err)
	}
	checkRecommendation(t, snapshot.recommend(domain.Provider{}, "gpt-fixture"), "context_window", "128000")
	if _, err = catalog.load(context.Background()); err != nil {
		t.Fatalf("validated in-memory snapshot was not cached: %v", err)
	}
}

func TestManualRecommendationDoesNotContactProvider(t *testing.T) {
	var providerRequests, publicRequests atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequests.Add(1)
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer providerServer.Close()
	publicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicRequests.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Private-Header") != "" {
			t.Error("provider credentials leaked to public catalog")
		}
		_, _ = w.Write([]byte(catalogFixture))
	}))
	defer publicServer.Close()
	b := testBridge(t)
	b.ModelCatalog = NewModelCatalog("", publicServer.Client())
	b.ModelCatalog.url = publicServer.URL
	ctx := context.Background()
	provider := domain.Provider{Name: "custom", Protocol: "openai-completions", Dialect: "compatible", BaseURL: providerServer.URL}
	key, headers := "private-key", map[string]string{"Private-Header": "secret-header"}
	if err := b.SaveProvider(ctx, &provider, &key, &headers); err != nil {
		t.Fatal(err)
	}
	id := "gateway/vendor/gpt-fixture"
	item, err := b.ModelRecommendation(ctx, provider.ID, id)
	if err != nil || item.ID != id || providerRequests.Load() != 0 || publicRequests.Load() != 1 {
		t.Fatalf("manual recommendation contacted provider or changed identity: %+v %v", item, err)
	}
	checkRecommendation(t, item, "context_window", "128000")
	if _, err = b.Catalog(ctx, provider.ID); err == nil {
		t.Fatal("failed live catalog was replaced with public inventory")
	}
	if _, err = b.ImportModels(ctx, provider.ID, ImportModelsInput{}); err == nil {
		t.Fatal("unselected models imported from public inventory")
	}
	imported, err := b.ImportModels(ctx, provider.ID, ImportModelsInput{IDs: []string{id}})
	if err != nil || len(imported.Imported) != 1 || imported.Imported[0].ModelID != id || imported.CatalogError == "" {
		t.Fatalf("explicit ID fallback failed: %+v %v", imported, err)
	}
	if len(imported.Imported[0].Overrides) != 0 || imported.Imported[0].Verification != nil {
		t.Fatal("fallback fabricated a request override or verification")
	}
	if !strings.Contains(imported.Imported[0].RecommendationSources["context_window"].Source, "models.dev") {
		t.Fatal("import lost public metadata provenance")
	}
}

func TestLiveCatalogMetadataHasPriorityOverPublicRecommendation(t *testing.T) {
	var providerRequests atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			t.Errorf("unexpected provider request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"openai/gpt-fixture","name":"Live model","context_length":null,"max_output_tokens":8192,"input_modalities":[],"capabilities":{"supports":{"tool_calls":false}},"reasoning":{"mandatory":false,"supported_efforts":[],"supports_max_tokens":false},"pricing":{"prompt":"0","completion":null}}]}`))
	}))
	defer providerServer.Close()
	publicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if providerRequests.Load() == 0 {
			t.Error("public directory was requested before live model discovery")
		}
		_, _ = w.Write([]byte(catalogFixture))
	}))
	defer publicServer.Close()
	b := testBridge(t)
	b.ModelCatalog = NewModelCatalog("", publicServer.Client())
	b.ModelCatalog.url = publicServer.URL
	ctx := context.Background()
	provider := domain.Provider{Name: "OpenRouter fixture", PresetID: "openrouter", Protocol: "openai-responses", Dialect: "openai", BaseURL: providerServer.URL}
	if err := b.SaveProvider(ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	items, err := b.Catalog(ctx, provider.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("live catalog: %+v %v", items, err)
	}
	item := items[0]
	for field, value := range map[string]string{
		"context_window": "null", "max_output_tokens": "8192", "tool_calling": `"unsupported"`, "images": `"unsupported"`,
		"reasoning.efforts": "[]", "reasoning.budget": "false", "reasoning.toggle": "true", "input_price": "0", "output_price": "null",
	} {
		checkRecommendation(t, item, field, value)
		if item.RecommendationSources[field].Source != providerServer.URL+"/models" {
			t.Fatalf("live field %s has wrong provenance", field)
		}
	}
	if _, exists := item.Recommendations["defaults"]; exists {
		t.Fatal("metadata discovery inserted generation options")
	}
}

func TestPublicReasoningMetadataDistinguishesMissingEmptyAndNull(t *testing.T) {
	for _, tc := range []struct {
		name, fields, field, expected string
	}{
		{"missing", `"reasoning":true`, "reasoning.efforts", ""},
		{"empty", `"reasoning":true,"reasoning_options":[]`, "reasoning.efforts", "[]"},
		{"null-options", `"reasoning":true,"reasoning_options":null`, "reasoning.efforts", "null"},
		{"null-reasoning", `"reasoning":null`, "reasoning", "null"},
		{"none-is-a-native-level", `"reasoning":true,"reasoning_options":[{"type":"effort","values":["none","high"]}]`, "reasoning.efforts", `["none","high"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := fixtureSnapshot(t, `{"fixture":{"models":{"model":{`+tc.fields+`}}}}`)
			item := snapshot.recommend(domain.Provider{}, "model")
			if tc.expected == "" {
				if _, exists := flattenRecommendations(item.Recommendations)[tc.field]; exists {
					t.Fatalf("unknown field %s was manufactured", tc.field)
				}
			} else {
				checkRecommendation(t, item, tc.field, tc.expected)
			}
			if _, exists := flattenRecommendations(item.Recommendations)["reasoning.off_effort"]; exists {
				t.Fatal("catalog inferred wire off semantics from a native effort string")
			}
		})
	}
}

func TestPresetCatalogUsesRuntimeSourceAndIndependentValues(t *testing.T) {
	b := testBridge(t)
	b.ModelCatalog = NewModelCatalog("", nil)
	b.ModelCatalog.loaded = true
	b.ModelCatalog.snapshot = fixtureSnapshot(t, catalogFixture)
	b.ModelCatalog.now = func() time.Time { return b.ModelCatalog.snapshot.FetchedAt }
	items, err := b.PresetCatalog(context.Background(), "openrouter")
	if err != nil || len(items) != 1 || items[0].ID != "openai/gpt-fixture" {
		t.Fatalf("preset source listing: %+v %v", items, err)
	}
	items[0].Recommendations["context_window"] = raw(1)
	again, err := b.PresetCatalog(context.Background(), "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	checkRecommendation(t, again[0], "context_window", "64000")
}

func TestCatalogEnrichmentPrioritizesKnownInterfaceThenLiveMetadata(t *testing.T) {
	snapshot := fixtureSnapshot(t, `{"xiaomi":{"models":{"mimo-v2.6-flash":{"name":"MiMo Flash","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}],"limit":{"context":64000,"output":8192}}}}}`)
	provider := domain.Provider{PresetID: "mimo", Protocol: "openai-responses", Dialect: "openai"}
	live := Discovery{ID: "mimo-v2.6-flash", Name: "mimo-v2.6-flash"}
	item := enrichDiscovery(provider, snapshot, live)
	checkRecommendation(t, item, "reasoning.efforts", "[]")
	checkRecommendation(t, item, "reasoning.on_effort", `"high"`)
	checkRecommendation(t, item, "reasoning.off_effort", `"none"`)
	checkRecommendation(t, item, "context_window", "64000")
	if !strings.Contains(item.RecommendationSources["reasoning.efforts"].Source, "mimo.mi.com") || !strings.Contains(item.RecommendationSources["context_window"].Source, "models.dev") {
		t.Fatal("known contract and public limit provenance were mixed")
	}
	live.Source = "https://gateway.example/models"
	live.Recommendations = map[string]json.RawMessage{"reasoning": raw(map[string]any{"toggle": false, "efforts": []string{"medium"}, "default_on": nil})}
	markRecommendationSources(&live, time.Now())
	item = enrichDiscovery(provider, snapshot, live)
	checkRecommendation(t, item, "reasoning.toggle", "false")
	checkRecommendation(t, item, "reasoning.efforts", `["medium"]`)
	checkRecommendation(t, item, "reasoning.default_on", "null")
	if item.RecommendationSources["reasoning.efforts"].Source != live.Source {
		t.Fatal("known rule overrode live field provenance")
	}
	for _, tc := range []struct {
		provider domain.Provider
		id       string
	}{
		{domain.Provider{Protocol: "openai-responses", Dialect: "openai"}, "mimo-v2.6-flash"},
		{provider, "vendor/mimo-v2.6-flash"},
	} {
		item := enrichDiscovery(tc.provider, snapshot, Discovery{ID: tc.id, Name: tc.id})
		checkRecommendation(t, item, "reasoning.efforts", `["low","high"]`)
		if _, exists := flattenRecommendations(item.Recommendations)["reasoning.on_effort"]; exists {
			t.Fatal("slug match applied a different interface's known request semantics")
		}
	}
}

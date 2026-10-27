package modelbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models/provider"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestVerificationRevisionAndImmutableCredentials(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var requests []map[string]any
	var auth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		requests = append(requests, req)
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req["tool_choice"] == "auto" {
			_, _ = w.Write([]byte(`{"id":"verify-1","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","reasoning_content":"preserve this native reasoning","tool_calls":[{"id":"verify-call","type":"function","function":{"name":"astral_verify","arguments":"{\"value\":\"ping\"}"}}]}}]}`))
		} else {
			_, _ = w.Write([]byte(`{"id":"verify-2","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"pong","reasoning_content":"complete"}}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`))
		}
	}))
	defer server.Close()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bridge := &Bridge{Store: store, HTTPClient: server.Client()}
	provider := domain.Provider{Name: "test", Protocol: "openai-completions", Dialect: "deepseek", BaseURL: server.URL}
	key := "test-old-key"
	if err = bridge.SaveProvider(ctx, &provider, &key, nil); err != nil {
		t.Fatal(err)
	}
	profile := domain.ModelProfile{ProviderID: provider.ID, ModelID: "test-model", Name: "test", Recommendations: map[string]json.RawMessage{
		"context_window": raw(128000), "max_output_tokens": raw(4096), "streaming": raw(false),
	}, Overrides: map[string]json.RawMessage{}}
	if err = bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	if selected, _, selectionErr := bridge.Selection(ctx, profile.ID); selectionErr != nil || selected != Reference(profile, provider) {
		t.Fatalf("new configuration was not immediately selectable: %q %v", selected, selectionErr)
	}
	verdict, err := bridge.Verify(ctx, profile.ID)
	if err != nil || verdict.ToolCalling != "supported" || verdict.ToolReplay != "supported" {
		t.Fatalf("verify %+v: %v", verdict, err)
	}
	ref, options, err := bridge.Selection(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	model, err := bridge.Resolve(ctx, ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Get(ctx, "models", profile.ID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Description = "A display-only edit"
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bridge.Selection(ctx, profile.ID); err != nil {
		t.Fatalf("description invalidated verification: %v", err)
	}
	for _, enabled := range []bool{false, true} {
		profile.Enabled = &enabled
		if err := bridge.SaveProfile(ctx, &profile); err != nil {
			t.Fatal(err)
		}
		_, _, selectionErr := bridge.Selection(ctx, profile.ID)
		if (selectionErr == nil) != enabled {
			t.Fatalf("model enable state %v: %v", enabled, selectionErr)
		}
	}
	for _, enabled := range []bool{false, true} {
		provider.Enabled = &enabled
		if err := bridge.SaveProvider(ctx, &provider, nil, nil); err != nil {
			t.Fatal(err)
		}
		_, _, selectionErr := bridge.Selection(ctx, profile.ID)
		if (selectionErr == nil) != enabled {
			t.Fatalf("provider enable state %v: %v", enabled, selectionErr)
		}
	}
	key = "test-new-key"
	if err = bridge.SaveProvider(ctx, &provider, &key, nil); err != nil {
		t.Fatal(err)
	}
	if selected, _, selectionErr := bridge.Selection(ctx, profile.ID); selectionErr != nil || selected == ref {
		t.Fatalf("changed provider was not immediately selectable: %q %v", selected, selectionErr)
	}
	request, err := model.(*boundModel).PrepareRequest(ctx, core.Request{ModelID: ref, SystemPrompt: "fixed rules", MaxOutputTokens: 100, Options: options, Messages: []core.Message{{Role: core.RoleUser, ContextKind: "astral.environment", Content: core.Text("current computer: original")}, core.User("continue")}})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := model.Stream(ctx, request, func(core.ModelEvent) {})
	if err != nil || reply.Message.FinishReason != core.FinishStop {
		t.Fatalf("immutable model snapshot failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auth) != 3 || auth[2] != "Bearer test-old-key" {
		t.Fatalf("in-flight model used mutated credentials: %v", auth)
	}
	if !strings.Contains(string(raw(requests[1])), "preserve this native reasoning") {
		t.Fatal("tool result verification did not replay reasoning")
	}
	if !strings.Contains(string(raw(requests[2])), "current computer: original") {
		t.Fatal("environment missing from the actual request")
	}
	if request.SystemPrompt != "fixed rules" {
		t.Fatal("preparation rewrote the stable system prompt")
	}
}

func TestCatalogProvidesNativeEffortsForImportedModel(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"flash","name":"Flash","context_window":1048576,"max_output_tokens":393216,"input_modalities":["text","image"],"effort":{"supported_levels":["low","high","max"],"default_level":"high"}}]}`))
	}))
	defer server.Close()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bridge := &Bridge{Store: store}
	provider := domain.Provider{Name: "test", Protocol: "openai-responses", Dialect: "deepseek", BaseURL: server.URL}
	if err = bridge.SaveProvider(ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	profile := domain.ModelProfile{ProviderID: provider.ID, ModelID: "flash", Name: "Flash", Recommendations: map[string]json.RawMessage{
		"context_window": raw(128000), "max_output_tokens": raw(8192), "reasoning": raw(map[string]any{"efforts": []string{"low"}}),
	}, Overrides: map[string]json.RawMessage{"max_output_tokens": raw(16384), "defaults": raw(core.GenerationOptions{Reasoning: core.ReasoningOptions{Effort: "low"}})}}
	if err = bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	catalog, err := bridge.Catalog(ctx, provider.ID)
	if err != nil || len(catalog) != 1 {
		t.Fatalf("catalog: %+v %v", catalog, err)
	}
	profile.Recommendations = catalog[0].Recommendations
	values, sources, err := profile.Effective()
	if err != nil || values.MaxOutputTokens != 16384 || values.ContextWindow != 1048576 || values.Defaults.Reasoning.Effort != "low" || sources["max_output_tokens"] != "override" {
		t.Fatalf("catalog overwrote overrides: %+v %v", values, err)
	}
	delete(profile.Overrides, "defaults")
	values, _, _ = profile.Effective()
	if values.Defaults.Reasoning.Effort != "" || !strings.Contains(string(values.Reasoning), `"default_effort":"high"`) || strings.Contains(string(values.Reasoning), "medium") {
		t.Fatal("native effort metadata changed or upstream default became a request override")
	}
}

func TestImagesArePreparedBySDKWithoutMutatingPersistentInput(t *testing.T) {
	ctx := context.Background()
	bridge := testBridge(t)
	var captured map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"seen"}}]}`))
	}))
	defer server.Close()
	connection := domain.Provider{Name: "images", Protocol: "openai-completions", Dialect: "compatible", BaseURL: server.URL}
	if err := bridge.SaveProvider(ctx, &connection, nil, nil); err != nil {
		t.Fatal(err)
	}
	profile := domain.ModelProfile{ProviderID: connection.ID, ModelID: "new-model", Name: "Images", Recommendations: map[string]json.RawMessage{"context_window": raw(10000), "max_output_tokens": raw(1000), "images": raw("supported"), "streaming": raw(false)}}
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	resolved, err := bridge.ResolveSelection(ctx, core.ModelSelection{ID: profile.ID})
	if err != nil {
		t.Fatal(err)
	}
	request := core.Request{Messages: []core.Message{{Role: core.RoleUser, Content: []core.Content{{Type: "image", MediaType: "image/png", URL: "astral-file://computer/file", Data: raw("ZnJvemVuLXBpeGVscw==")}}}}}
	if _, err := provider.Call(ctx, resolved.Model, request, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(captured["messages"]), "data:image/png;base64,ZnJvemVuLXBpeGVscw==") || request.Messages[0].Content[0].URL != "astral-file://computer/file" || len(request.Messages[0].Content[0].Data) == 0 {
		t.Fatal("transient SDK media preparation mutated persistent pixels")
	}
	request.Messages[0].Content[0].Data = nil
	if _, err := provider.Call(ctx, resolved.Model, request, nil); err == nil {
		t.Fatal("reference without frozen pixels was accepted")
	}
}

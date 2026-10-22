package harness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/search"
)

func TestSearchToolPassesOptionsAndCompleteResultsToModel(t *testing.T) {
	var calls atomic.Int64
	fullText := strings.Repeat("Search evidence. ", 500) + "END-OF-FULL-SEARCH-TEXT"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		domains, ok := body["include_domains"].([]any)
		if body["max_results"] != float64(37) || !ok || len(domains) != 1 || domains[0] != "example.com" || body["start_date"] == nil {
			t.Errorf("tool options were lost: %#v", body)
		}
		results := make([]search.Result, 8)
		for i := range results {
			results[i] = search.Result{Title: "Source", URL: "https://example.com", Summary: fullText}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer provider.Close()
	var sawResult atomic.Bool
	f := newFixture(t, 0, func(_ context.Context, n int, request map[string]any) any {
		if n == 1 {
			found := false
			for _, raw := range request["tools"].([]any) {
				tool := raw.(map[string]any)["function"].(map[string]any)
				if tool["name"] != "web_search" {
					continue
				}
				found = true
				schema := tool["parameters"].(map[string]any)
				props := schema["properties"].(map[string]any)
				for _, name := range []string{"query", "domains", "recency", "limit"} {
					if props[name] == nil {
						t.Errorf("missing model-facing parameter %s", name)
					}
				}
				if props["limit"].(map[string]any)["default"] != nil {
					t.Error("tool schema injects a result default")
				}
			}
			if !found {
				t.Error("web_search was not exposed to model")
			}
			return toolReply("search-call", "web_search", map[string]any{"query": "query", "domains": []string{"example.com"}, "limit": 37, "recency": 14})
		}
		if n != 2 {
			t.Errorf("unexpected additional model request: %d", n)
		}
		for _, raw := range request["messages"].([]any) {
			message := raw.(map[string]any)
			if message["role"] != "tool" {
				continue
			}
			content, ok := message["content"].(string)
			if !ok {
				t.Error("tool result was not text")
				continue
			}
			var response search.Response
			if err := json.Unmarshal([]byte(content), &response); err != nil {
				t.Error(err)
				continue
			}
			if len(response.Results) != 8 {
				t.Errorf("model received %d results, want 8", len(response.Results))
				continue
			}
			for _, result := range response.Results {
				if result.Summary != fullText {
					t.Error("model received truncated search text")
				}
			}
			sawResult.Store(true)
		}
		return textReply("done")
	})
	p := domain.SearchProvider{Name: "Search fixture", Adapter: "tavily", BaseURL: provider.URL}
	if err := f.service.Search.Save(f.ctx, &p, map[string]string{"api_key": "key"}); err != nil {
		t.Fatal(err)
	}
	f.agent.SearchProviderID = p.ID
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "search", Text: "Search for evidence"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if calls.Load() != 1 || !sawResult.Load() {
		t.Fatalf("search did not complete: calls=%d, model received result=%v", calls.Load(), sawResult.Load())
	}
}

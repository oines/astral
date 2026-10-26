package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestExactlyOneSelectedProviderAndNoFallbackOnFailure(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var first, second atomic.Int64
	fail := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Add(1)
		if r.Header.Get("Authorization") != "Bearer tavily-key" {
			t.Error("missing Tavily bearer credential")
		}
		if fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"title": "Source", "url": "https://example.test/source", "content": "Evidence", "published_date": "2026-10-21"}}})
	}))
	defer server.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
		if r.Header.Get("x-api-key") != "exa-key" {
			t.Error("missing Exa credential")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		contents, _ := body["contents"].(map[string]any)
		if body["query"] != "query" || len(body) != 2 || len(contents) != 1 || contents["highlights"] != true {
			t.Error("invalid Exa request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"title": "Exa", "url": "https://example.test/exa", "highlights": []string{"Highlight"}, "author": "Author"}}})
	}))
	defer fallback.Close()
	ctx := context.Background()
	s := &Service{Store: store}
	one, two := domain.SearchProvider{Name: "Tavily", Adapter: "tavily", BaseURL: server.URL}, domain.SearchProvider{Name: "Exa", Adapter: "exa", BaseURL: fallback.URL}
	a, b := "tavily-key", "exa-key"
	if err = s.Save(ctx, &one, map[string]string{"api_key": a}); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(ctx, &two, map[string]string{"api_key": b}); err != nil {
		t.Fatal(err)
	}
	agent := domain.Agent{Meta: domain.Meta{ID: "agent"}, SearchProviderID: one.ID}
	if err = store.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
		t.Fatal(err)
	}
	result, err := s.Search(ctx, agent.ID, Request{Query: "query"})
	if err != nil || len(result.Results) != 1 || result.Results[0].Summary != "Evidence" {
		t.Fatalf("search contract failed: %+v %v", result, err)
	}
	fail.Store(true)
	if _, err = s.Search(ctx, agent.ID, Request{Query: "query"}); err == nil {
		t.Fatal("provider failure was hidden")
	}
	if first.Load() != 2 || second.Load() != 0 {
		t.Fatal("search retried or fell back")
	}
	agent.SearchProviderID = two.ID
	if err = store.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
		t.Fatal(err)
	}
	result, err = s.Search(ctx, agent.ID, Request{Query: "query"})
	if err != nil || len(result.Results) != 1 || result.Results[0].Summary != "Highlight" || second.Load() != 1 {
		t.Fatal("selected Exa was not used")
	}
}

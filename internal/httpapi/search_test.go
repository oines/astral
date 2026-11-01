package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/search"
	"github.com/oines/astral/internal/testkit"
)

func TestSearchCatalogCredentialsAndTestOptions(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	catalog := request(t, p.Context, server, "GET", "/api/v1/search-adapters", nil, true)
	var adapters []search.Adapter
	err := json.NewDecoder(catalog.Body).Decode(&adapters)
	catalog.Body.Close()
	if err != nil || catalog.StatusCode != 200 || len(adapters) != 12 {
		t.Fatalf("catalog: %d %v", catalog.StatusCode, err)
	}
	for _, adapter := range adapters {
		if adapter.ID == "bing" {
			t.Fatal("retired Bing adapter exposed")
		}
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["query"] != "latest news site:example.com" || body["count"] != float64(25) {
			t.Errorf("test endpoint lost options: %#v", body)
		}
		if r.Header.Get("Authorization") != "Bearer private-search-key" {
			t.Error("credential not supplied")
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"Source","url":"https://example.com","snippet":"Evidence"}]}`))
	}))
	defer backend.Close()
	response := request(t, p.Context, server, "POST", "/api/v1/search-providers", bytes.NewReader(testkit.JSON(map[string]any{"name": "DeepSeek", "adapter": "deepseek", "base_url": backend.URL, "credentials": map[string]string{"api_key": "private-search-key"}})), true)
	var provider domain.SearchProvider
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if err = json.Unmarshal(raw, &provider); err != nil || response.StatusCode != 200 {
		t.Fatalf("save: %d %s %v", response.StatusCode, raw, err)
	}
	if strings.Contains(string(raw), "private-search-key") {
		t.Fatal("API returned the secret")
	}
	response = request(t, p.Context, server, "POST", "/api/v1/search-providers/"+provider.ID+"/test", strings.NewReader(`{"query":"latest news","domains":["example.com"],"limit":25}`), true)
	raw, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(raw), "Evidence") {
		t.Fatalf("test request: %d %s", response.StatusCode, raw)
	}
	response = request(t, p.Context, server, "POST", "/api/v1/search-providers/"+provider.ID+"/test", strings.NewReader(`{"query":"news","limit":0}`), true)
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("invalid limit accepted: %d", response.StatusCode)
	}
	response = request(t, p.Context, server, "POST", "/api/v1/search-providers", strings.NewReader(`{"name":"legacy client","adapter":"tavily","api_key":"legacy-api-key"}`), true)
	raw, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || strings.Contains(string(raw), "legacy-api-key") {
		t.Fatalf("legacy api_key compatibility: %d", response.StatusCode)
	}
}

func TestDeleteSearchProviderDisablesReferencingBots(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	provider := domain.SearchProvider{Meta: domain.Meta{ID: "search-to-delete"}, Name: "Search"}
	if err := p.Store.Save(p.Context, "search", &provider.Meta, &provider); err != nil {
		t.Fatal(err)
	}
	bot := p.Agent
	bot.SearchProviderID = provider.ID
	if err := p.Harness.SaveAgent(p.Context, &bot); err != nil {
		t.Fatal(err)
	}
	pathPrefix := "/api/v1/search-providers/" + provider.ID + "?revision="
	path := pathPrefix + strconv.FormatInt(provider.Revision, 10)
	for _, check := range []struct {
		name   string
		path   string
		auth   bool
		status int
	}{
		{"unauthenticated", path, false, http.StatusUnauthorized},
		{"stale-confirmation", pathPrefix + strconv.FormatInt(provider.Revision+1, 10), true, http.StatusConflict},
		{"confirmed-delete", path, true, http.StatusOK},
	} {
		t.Run(check.name, func(t *testing.T) {
			response := request(t, p.Context, server, "DELETE", check.path, nil, check.auth)
			raw, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != check.status {
				t.Fatalf("delete: %d %s %v", response.StatusCode, raw, err)
			}
			var saved domain.Agent
			if err := p.Store.Get(p.Context, "agents", bot.ID, &saved); err != nil {
				t.Fatal(err)
			}
			if check.status == http.StatusOK {
				if saved.SearchProviderID != "" || saved.Revision != bot.Revision+1 || saved.Name != bot.Name {
					t.Fatalf("API deletion did not disable Bot search: %+v", saved)
				}
			} else if saved.SearchProviderID != provider.ID || saved.Revision != bot.Revision {
				t.Fatalf("rejected API deletion changed Bot search: %+v", saved)
			}
		})
	}
	var removed domain.SearchProvider
	if err := p.Store.Get(p.Context, "search", provider.ID, &removed); !domain.IsMissing(err) {
		t.Fatal("API deletion retained search service:", err)
	}
}

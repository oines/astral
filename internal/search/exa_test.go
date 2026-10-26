package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestExaRequestsExcerptsWithoutChangingSearchOptions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input Request
		want  map[string]any
	}{
		{
			name:  "provider defaults",
			input: Request{Query: "query"},
			want:  map[string]any{"query": "query", "contents": map[string]any{"highlights": true}},
		},
		{
			name: "explicit search options",
			input: Request{Query: "query", Limit: pointer(123), Recency: pointer(14),
				Domains: []string{"example.com", "docs.example.com"}},
			want: map[string]any{"query": "query", "contents": map[string]any{"highlights": true},
				"numResults": float64(123), "startPublishedDate": "2026-10-08T00:00:00.000Z",
				"includeDomains": []any{"example.com", "docs.example.com"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, body, params := wireRequest(t, "exa", tc.input)
			if !reflect.DeepEqual(body, tc.want) || len(params) != 0 {
				t.Fatalf("unexpected Exa request: body=%#v params=%v", body, params)
			}
		})
	}
}

func TestExaSearchRequestsContentAndPreservesEveryExcerpt(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const count = 11
	excerpt := strings.Repeat("original source excerpt 原文片段 ", 1000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" || r.Header.Get("x-api-key") != "test-exa-key" {
			t.Error("wrong Exa endpoint or authentication")
		}
		var request struct {
			Query    string `json:"query"`
			Contents struct {
				Highlights bool `json:"highlights"`
			} `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Query != "query" {
			t.Error("query changed")
		}
		results := make([]map[string]any, count)
		for i := range results {
			results[i] = map[string]any{
				"title": fmt.Sprintf("Source %d", i), "url": fmt.Sprintf("https://example.test/%d", i),
				"publishedDate": "2026-10-22T12:30:00Z", "author": "Source author",
			}
			// Exa's bare /search response contains metadata only. Content is
			// supplied only when the client explicitly asks for it.
			if request.Contents.Highlights {
				results[i]["highlights"] = []string{fmt.Sprintf("Source %d excerpt", i), excerpt}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer server.Close()
	service := Service{Store: store}
	provider := domain.SearchProvider{Name: "Exa", Adapter: "exa", BaseURL: server.URL}
	if err := service.Save(ctx, &provider, map[string]string{"api_key": "test-exa-key"}); err != nil {
		t.Fatal(err)
	}
	response, err := service.Query(ctx, provider.ID, Request{Query: "query"})
	if err != nil {
		t.Fatal(err)
	}
	if response.ProviderID != provider.ID || response.Query != "query" || len(response.Results) != count {
		t.Fatalf("unexpected Exa results: provider=%q query=%q count=%d", response.ProviderID, response.Query, len(response.Results))
	}
	for i, result := range response.Results {
		if result.Summary != fmt.Sprintf("Source %d excerpt\n", i)+excerpt {
			t.Fatalf("source %d excerpts missing or truncated", i)
		}
		if result.Title != fmt.Sprintf("Source %d", i) || result.URL != fmt.Sprintf("https://example.test/%d", i) ||
			result.PublishedAt != "2026-10-22T12:30:00Z" || result.Author != "Source author" {
			t.Fatalf("source %d metadata changed: %+v", i, result)
		}
	}
}

func TestExaParsesDocumentedContentWithoutInventingMissingText(t *testing.T) {
	longText := strings.Repeat("full text 正文 ", 1000)
	for _, tc := range []struct {
		name    string
		content map[string]any
		want    string
	}{
		{"highlights", map[string]any{"highlights": []string{"First source passage.", "Second source passage."}}, "First source passage.\nSecond source passage."},
		{"text", map[string]any{"text": longText}, longText},
		{"summary", map[string]any{"summary": "Provider-supplied summary."}, "Provider-supplied summary."},
		{"metadata only", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := map[string]any{"title": "Source title", "url": "https://example.test/source"}
			for name, value := range tc.content {
				item[name] = value
			}
			body, err := json.Marshal(map[string]any{"results": []any{item}})
			if err != nil {
				t.Fatal(err)
			}
			results, err := parseResults("exa", body)
			if err != nil || len(results) != 1 || results[0].Summary != tc.want {
				t.Fatalf("unexpected Exa content: count=%d error=%v", len(results), err)
			}
		})
	}
}

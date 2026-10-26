package search

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func yandexResponseFixture(xml string) string {
	return `{"rawData":"` + base64.StdEncoding.EncodeToString([]byte(xml)) + `"}`
}

func TestSearchProvidersShareStableResponseJSON(t *testing.T) {
	// These use each provider's documented wire names/envelope rather than
	// a fabricated universal upstream response. The Agent sees only Result.
	const source = "https://example.test/source"
	const title = "Source"
	const summary = "Source excerpt."
	const published = "2026-10-22"
	tests := []struct {
		id, body, empty, malformed, failure string
		want                                Result
	}{
		{
			id: "tavily", body: `{"results":[{"title":"Source","url":"https://example.test/source","content":"Source excerpt."}]}`,
			empty: `{"results":[]}`, malformed: `{"results":{}}`, failure: `{"error":"invalid API key","results":[]}`,
			want: Result{Title: title, URL: source, Summary: summary},
		},
		{
			id: "exa", body: `{"results":[{"title":"Source","url":"https://example.test/source","highlights":["Source excerpt."],"publishedDate":"2026-10-22","author":"Writer"}]}`,
			empty: `{"results":[]}`, malformed: `{"results":null}`, failure: `{"error":"invalid API key","results":[]}`,
			want: Result{Title: title, URL: source, Summary: summary, PublishedAt: published, Author: "Writer"},
		},
		{
			id: "deepseek", body: `{"results":[{"title":"Source","url":"https://example.test/source","summary":"Source excerpt."}]}`,
			empty: `{"results":[]}`, malformed: `{"results":["not an object"]}`, failure: `{"error":{"message":"invalid API key"},"results":[]}`,
			want: Result{Title: title, URL: source, Summary: summary},
		},
		{
			id: "brave", body: `{"web":{"results":[{"title":"Source","url":"https://example.test/source","description":"Source excerpt.","age":"2026-10-22"}]}}`,
			empty: `{"type":"search","query":{"original":"query"}}`, malformed: `{"web":[]}`, failure: `{"type":"ErrorResponse","error":{"detail":"invalid API key"},"query":{"original":"query"}}`,
			want: Result{Title: title, URL: source, Summary: summary, PublishedAt: published},
		},
		{
			id: "google", body: `{"items":[{"title":"Source","link":"https://example.test/source","snippet":"Source excerpt."}]}`,
			empty: `{"searchInformation":{"totalResults":"0"}}`, malformed: `{"items":{}}`, failure: `{"error":{"code":403,"message":"invalid API key"},"searchInformation":{"totalResults":"0"}}`,
			want: Result{Title: title, URL: source, Summary: summary},
		},
		{
			id: "sogou", body: `{"Response":{"Pages":["{\"title\":\"Source\",\"url\":\"https://example.test/source\",\"passage\":\"Source excerpt.\",\"date\":\"2026-10-22\"}"],"Query":"query","RequestId":"request"}}`,
			empty: `{"Response":{"Pages":[],"Query":"query","RequestId":"request"}}`, malformed: `{"Response":{"Pages":[false]}}`, failure: `{"Response":{"Error":{"Code":"AuthFailure","Message":"invalid credential"},"Pages":[]}}`,
			want: Result{Title: title, URL: source, Summary: summary, PublishedAt: published},
		},
		{
			id: "serper", body: `{"organic":[{"title":"Source","link":"https://example.test/source","snippet":"Source excerpt."}]}`,
			empty: `{"organic":[]}`, malformed: `{"organic":{}}`, failure: `{"message":"Unauthorized.","statusCode":403,"organic":[]}`,
			want: Result{Title: title, URL: source, Summary: summary},
		},
		{
			id: "searxng", body: `{"query":"query","results":[{"title":"Source","url":"https://example.test/source","content":"Source excerpt."}]}`,
			empty: `{"query":"query","results":[]}`, malformed: `{"results":null}`, failure: `{"error":"search failed","results":[]}`,
			want: Result{Title: title, URL: source, Summary: summary},
		},
		{
			id: "jina", body: `{"code":200,"status":20000,"data":[{"title":"Source","url":"https://example.test/source","content":"Source excerpt.","publishedTime":"2026-10-22"}]}`,
			empty: `{"code":200,"status":20000,"data":[]}`, malformed: `{"code":"200","status":20000,"data":[]}`, failure: `{"code":401,"status":40102,"message":"invalid API key","data":[]}`,
			want: Result{Title: title, URL: source, Summary: summary, PublishedAt: published},
		},
		{
			id: "bocha", body: `{"code":200,"data":{"webPages":{"value":[{"name":"Source","url":"https://example.test/source","snippet":"Source excerpt.","datePublished":"2026-10-22"}]}}}`,
			empty: `{"code":200,"data":{"webPages":{"value":[]}}}`, malformed: `{"code":200,"data":{"webPages":{"value":{}}}}`, failure: `{"code":403,"msg":"invalid API key","data":{"webPages":{"value":[]}}}`,
			want: Result{Title: title, URL: source, Summary: summary, PublishedAt: published},
		},
		{
			id: "duckduckgo", body: `<div class="result results_links"><a class="result__a" href="https://example.test/source">Source</a><a class="result__snippet">Source excerpt.</a></div>`,
			empty: `<div class="no-results">No results found</div>`, malformed: `<html><body>Unknown page</body></html>`, failure: `<form id="challenge-form"><div class="anomaly-modal">Verify</div></form>`,
			want: Result{Title: title, URL: source, Summary: summary},
		},
		{
			id: "yandex", body: yandexResponseFixture(`<yandexsearch><response><results><grouping><group><doc><url>https://example.test/source</url><title>Source</title><modtime>2026-10-22</modtime><passages><passage>Source excerpt.</passage></passages></doc></group></grouping></results></response></yandexsearch>`),
			empty: yandexResponseFixture(`<yandexsearch><response><error code="15">No matching results</error></response></yandexsearch>`), malformed: yandexResponseFixture(`<yandexsearch><response>`), failure: yandexResponseFixture(`<yandexsearch><response><error code="32">Invalid key</error></response></yandexsearch>`),
			want: Result{Title: title, URL: source, Summary: summary, PublishedAt: published},
		},
	}
	if len(tests) != len(adapters) {
		t.Fatal("response contract cases and adapter catalog differ")
	}
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := Service{Store: store}
	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			var body atomic.Value
			body.Store(tc.body)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body.Load().(string))
			}))
			defer server.Close()
			provider := domain.SearchProvider{Name: tc.id, Adapter: tc.id, BaseURL: server.URL,
				Config: map[string]string{}}
			keys := map[string]string{}
			adapter, _ := adapterByID(tc.id)
			for _, field := range adapter.Fields {
				if field.Secret && field.Required {
					keys[field.Name] = "test-secret"
				} else if field.Required {
					provider.Config[field.Name] = "test-config"
				}
			}
			if err := service.Save(ctx, &provider, keys); err != nil {
				t.Fatal(err)
			}
			query := func() (Response, error) {
				return service.Query(ctx, provider.ID, Request{Query: "  query  "})
			}
			response, err := query()
			if err != nil || !reflect.DeepEqual(response.Results, []Result{tc.want}) {
				t.Fatalf("incorrect normalized result: %+v error=%v", response.Results, err)
			}
			assertSearchResponseJSON(t, response, provider.ID, []Result{tc.want})
			body.Store(tc.empty)
			response, err = query()
			if err != nil || response.Results == nil || len(response.Results) != 0 {
				t.Fatalf("legitimate zero results must return []: %+v error=%v", response, err)
			}
			assertSearchResponseJSON(t, response, provider.ID, []Result{})
			for _, invalid := range []string{tc.malformed, tc.failure} {
				body.Store(invalid)
				if response, err := query(); err == nil {
					t.Fatalf("provider failure was reported as success: %+v", response)
				}
			}
		})
	}
}

func assertSearchResponseJSON(t *testing.T, response Response, providerID string, want []Result) {
	t.Helper()
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 3 || wire["provider_id"] != providerID || wire["query"] != "query" {
		t.Fatalf("unstable search response envelope: %s", raw)
	}
	results, ok := wire["results"].([]any)
	if !ok || len(results) != len(want) {
		t.Fatalf("results must be a JSON array: %s", raw)
	}
	for i, value := range results {
		result, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("result must be a JSON object: %s", raw)
		}
		expected := map[string]string{"title": want[i].Title, "url": want[i].URL, "summary": want[i].Summary}
		if want[i].PublishedAt != "" {
			expected["published_at"] = want[i].PublishedAt
		}
		if want[i].Author != "" {
			expected["author"] = want[i].Author
		}
		if len(result) != len(expected) {
			t.Fatalf("unstable result fields: %s", raw)
		}
		for key, want := range expected {
			value, ok := result[key].(string)
			if !ok || value != want {
				t.Fatalf("result %s must be the normalized string value: %s", key, raw)
			}
		}
	}
}

func TestSearchContentSkipsWhitespaceWithoutChangingSourceText(t *testing.T) {
	const text = " \nFull source text with preserved whitespace.\n "
	for _, tc := range []struct {
		name string
		item searchItem
		want string
	}{
		{"blank content with snippet", searchItem{Content: " \t\n", Snippet: text}, text},
		{"blank text with highlights", searchItem{Text: "\n", Highlights: []string{text, "Second passage"}}, text + "\nSecond passage"},
		{"Jina chunks", searchItem{Content: "\t", Chunks: []string{text, "Second chunk"}}, text + "\nSecond chunk"},
		{"full text with extra snippets", searchItem{Content: text, ExtraSnippets: []string{"\t", " Extra source snippet. "}}, text + "\n Extra source snippet. "},
		{"extra snippets only", searchItem{ExtraSnippets: []string{"\t", text}}, text},
		{"no content", searchItem{Content: " \t", Summary: "\n", Highlights: []string{"\t"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.item.result()
			if result.Summary != tc.want {
				t.Fatalf("source text changed: got %q want %q", result.Summary, tc.want)
			}
			assertSearchResponseJSON(t, Response{ProviderID: "provider", Query: "query", Results: []Result{result}}, "provider", []Result{result})
		})
	}
}

func TestSearchRejectsMalformedEnvelopesAndItems(t *testing.T) {
	for _, tc := range []struct{ id, body string }{
		{"tavily", `{"results":[null]}`},
		{"exa", "{\"results\": \n null }"},
		{"brave", `{"web":null,"query":{"original":"query"}}`},
		{"brave", `{"web":{"results":[null]}}`},
		{"brave", `{"query":true}`},
		{"google", `{"searchInformation":null}`},
		{"google", `{"searchInformation":true}`},
		{"google", `{"searchInformation":{"totalResults":"8"}}`},
		{"jina", `{"code":200,"status":"40102","data":[]}`},
		{"jina", `{"code":200,"status":40102,"message":"invalid credential","data":[]}`},
		{"jina", `{"code":null,"data":[]}`},
		{"serper", `{"statusCode":"403","organic":[]}`},
		{"serper", `{"statusCode":null,"organic":[]}`},
		{"bocha", `{"code":200,"data":{"webPages":{"value":[null]}}}`},
		{"bocha", `{"code":0,"data":{"webPages":{"value":[]}}}`},
		{"bocha", `{"code":null,"data":{"webPages":{"value":[]}}}`},
		{"yandex", yandexResponseFixture(`<yandexsearch><response/></yandexsearch>`)},
	} {
		t.Run(tc.id+"/"+tc.body, func(t *testing.T) {
			if results, err := parseResults(tc.id, []byte(tc.body)); err == nil {
				t.Fatalf("malformed response became success: %+v", results)
			}
		})
	}
}

func TestSearchPermittedAbsentResultSectionsReturnArrays(t *testing.T) {
	for _, tc := range []struct{ id, body string }{
		{"brave", `{"type":"search","query":{"original":"query"},"web":null}`},
		{"brave", `{"web":{"results":[]}}`},
		{"google", `{"items":[]}`},
		{"bocha", `{"data":{}}`},
		{"bocha", `{"code":200,"data":{"webPages":null}}`},
		{"searxng", `{"results":[],"unresponsive_engines":[["one engine","timeout"]]}`},
	} {
		t.Run(tc.id+"/"+tc.body, func(t *testing.T) {
			results, err := parseResults(tc.id, []byte(tc.body))
			if err != nil || results == nil || len(results) != 0 {
				t.Fatalf("valid empty response was rejected or became null: %+v error=%v", results, err)
			}
			assertSearchResponseJSON(t, Response{ProviderID: "provider", Query: "query", Results: results}, "provider", []Result{})
		})
	}
}

func TestSearchMissingContentRemainsAnEmptyString(t *testing.T) {
	results, err := parseResults("exa", []byte(`{"results":[{"title":"Source","url":"https://example.test/source"}]}`))
	if err != nil || len(results) != 1 || strings.TrimSpace(results[0].Summary) != "" {
		t.Fatalf("missing content was invented or result lost: %+v %v", results, err)
	}
	assertSearchResponseJSON(t, Response{ProviderID: "provider", Query: "query", Results: results}, "provider", results)
}

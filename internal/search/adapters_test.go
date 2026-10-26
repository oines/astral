package search

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func pointer(value int) *int { return &value }

var searchTime = time.Date(2026, 10, 22, 12, 30, 0, 0, time.UTC)

func wireRequest(t *testing.T, adapter string, input Request) (*http.Request, map[string]any, url.Values) {
	t.Helper()
	spec, ok := adapterByID(adapter)
	if !ok {
		t.Fatal("missing adapter", adapter)
	}
	normalized, err := normalizeRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	p := domain.SearchProvider{Adapter: adapter, BaseURL: "http://localhost:1234", Config: map[string]string{"cx": "engine", "folder_id": "folder"}}
	req, err := buildRequest(context.Background(), spec, p, map[string]string{"api_key": "key", "secret_id": "id", "secret_key": "secret"}, normalized, searchTime)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{}
	params := req.URL.Query()
	if req.Body != nil {
		raw, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if req.Header.Get("Content-Type") == "application/json" {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
		} else if len(raw) > 0 {
			params, err = url.ParseQuery(string(raw))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return req, body, params
}

func TestEveryAdapterOmitsOptionalDefaults(t *testing.T) {
	tests := []struct {
		id, method, path, header, auth string
		body                           map[string]any
		params                         url.Values
	}{
		{"tavily", "POST", "/search", "Authorization", "Bearer key", map[string]any{"query": "query"}, nil},
		{"exa", "POST", "/search", "x-api-key", "key", map[string]any{"query": "query", "contents": map[string]any{"highlights": true}}, nil},
		{"deepseek", "POST", "/v1/tools/search", "Authorization", "Bearer key", map[string]any{"query": "query"}, nil},
		{"brave", "GET", "/res/v1/web/search", "X-Subscription-Token", "key", nil, url.Values{"q": {"query"}}},
		{"google", "GET", "/customsearch/v1", "Authorization", "", nil, url.Values{"q": {"query"}, "key": {"key"}, "cx": {"engine"}}},
		{"sogou", "POST", "/", "X-TC-Action", "SearchPro", map[string]any{"Query": "query"}, nil},
		{"serper", "POST", "/search", "X-API-KEY", "key", map[string]any{"q": "query"}, nil},
		{"searxng", "GET", "/search", "Authorization", "Bearer key", nil, url.Values{"q": {"query"}, "format": {"json"}}},
		{"jina", "GET", "/search", "Authorization", "Bearer key", nil, url.Values{"q": {"query"}}},
		{"bocha", "POST", "/v1/web-search", "Authorization", "Bearer key", map[string]any{"query": "query", "summary": true}, nil},
		{"duckduckgo", "POST", "/html/", "Authorization", "", nil, url.Values{"q": {"query"}}},
		{"yandex", "POST", "/v2/web/search", "Authorization", "Api-Key key", map[string]any{"query": map[string]any{"searchType": "SEARCH_TYPE_COM", "queryText": "query"}, "folderId": "folder"}, nil},
	}
	if len(adapters) != len(tests) {
		t.Fatal("catalog and tested adapters differ")
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			req, body, params := wireRequest(t, tc.id, Request{Query: "  query  "})
			if req.Method != tc.method || req.URL.Path != tc.path || req.Header.Get(tc.header) != tc.auth {
				t.Fatalf("wrong request: %s %s %v", req.Method, req.URL, req.Header)
			}
			if len(body) != len(tc.body) || len(tc.body) > 0 && !reflect.DeepEqual(body, tc.body) {
				t.Fatalf("unexpected defaults in JSON: %#v", body)
			}
			if len(params) != len(tc.params) || len(tc.params) > 0 && !reflect.DeepEqual(params, tc.params) {
				t.Fatalf("unexpected query defaults: %#v", params)
			}
			if tc.id == "sogou" && (!strings.HasPrefix(req.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=id/2026-10-22/wsa/tc3_request,") || req.Header.Get("X-TC-Version") != "2025-05-08") {
				t.Fatal("Tencent signature headers missing")
			}
		})
	}
}

func TestNativeOptionsArePassedWithoutClamping(t *testing.T) {
	input := Request{Query: "query", Domains: []string{"EXAMPLE.COM", "example.com", "文档.cn"}, Limit: pointer(123), Recency: pointer(14)}
	domains := []string{"example.com", "xn--7dvp7d.cn"}
	for _, id := range []string{"tavily", "exa", "deepseek", "brave", "google", "serper", "bocha", "yandex"} {
		t.Run(id, func(t *testing.T) {
			_, body, params := wireRequest(t, id, input)
			switch id {
			case "tavily":
				if body["max_results"] != float64(123) || body["start_date"] != "2026-10-08" || !reflect.DeepEqual(body["include_domains"], []any{domains[0], domains[1]}) {
					t.Fatalf("bad options: %#v", body)
				}
			case "exa":
				if body["numResults"] != float64(123) || body["startPublishedDate"] != "2026-10-08T00:00:00.000Z" || !reflect.DeepEqual(body["includeDomains"], []any{domains[0], domains[1]}) {
					t.Fatalf("bad options: %#v", body)
				}
			case "deepseek":
				if body["count"] != float64(123) || body["query"] != "query (site:example.com OR site:xn--7dvp7d.cn) after:2026-10-08" {
					t.Fatalf("bad options: %#v", body)
				}
			case "brave":
				if params.Get("count") != "123" || params.Get("freshness") != "2026-10-08to2026-10-22" || !strings.Contains(params.Get("q"), "(site:") {
					t.Fatalf("bad options: %#v", params)
				}
			case "google":
				if params.Get("num") != "123" || params.Get("dateRestrict") != "d14" || !strings.Contains(params.Get("q"), "(site:") {
					t.Fatalf("bad options: %#v", params)
				}
			case "serper":
				if body["num"] != float64(123) || body["tbs"] != "qdr:d14" || !strings.Contains(body["q"].(string), "(site:") {
					t.Fatalf("bad options: %#v", body)
				}
			case "bocha":
				if body["count"] != float64(123) || body["freshness"] != "2026-10-08..2026-10-22" || body["include"] != strings.Join(domains, ",") {
					t.Fatalf("bad options: %#v", body)
				}
			case "yandex":
				if body["period"] != "PERIOD_2_WEEKS" || !reflect.DeepEqual(body["groupSpec"], map[string]any{"groupMode": "GROUP_MODE_FLAT", "groupsOnPage": "123"}) || body["query"].(map[string]any)["queryText"] != "query (site:example.com | site:xn--7dvp7d.cn)" {
					t.Fatalf("bad options: %#v", body)
				}
			}
		})
	}
	_, body, _ := wireRequest(t, "sogou", Request{Query: "query", Domains: []string{"example.com"}, Limit: pointer(40), Recency: pointer(90)})
	if body["Cnt"] != float64(40) || body["Site"] != "example.com" || body["Freshness"] != "2026-07-24,2026-10-22" {
		t.Fatal(body)
	}
	_, _, params := wireRequest(t, "jina", Request{Query: "query", Limit: pointer(123), Domains: []string{"example.com"}})
	if params.Get("count") != "123" || params.Get("q") != "query site:example.com" {
		t.Fatal(params)
	}
}

func TestUnsupportedOptionsAndInvalidDomainsFailExplicitly(t *testing.T) {
	for _, test := range []struct {
		id      string
		request Request
	}{
		{"searxng", Request{Query: "q", Limit: pointer(5)}}, {"duckduckgo", Request{Query: "q", Limit: pointer(5)}},
		{"jina", Request{Query: "q", Recency: pointer(1)}}, {"searxng", Request{Query: "q", Recency: pointer(3)}},
		{"duckduckgo", Request{Query: "q", Recency: pointer(3)}}, {"yandex", Request{Query: "q", Recency: pointer(3)}},
		{"sogou", Request{Query: "q", Domains: []string{"a.test", "b.test"}}},
	} {
		spec, _ := adapterByID(test.id)
		_, err := buildRequest(context.Background(), spec, domain.SearchProvider{BaseURL: "https://search.example.com"}, nil, test.request, searchTime)
		if err == nil || !strings.Contains(err.Error(), "does not support") {
			t.Fatalf("%s ignored unsupported options: %v", test.id, err)
		}
	}
	for _, name := range []string{"https://example.com", "example.com/a", "example.com:443", "127.0.0.1", "[::1]", "", "*.example.com", "-bad.test", "bad..test"} {
		if _, err := normalizeRequest(Request{Query: "q", Domains: []string{name}}); err == nil {
			t.Errorf("invalid domain accepted: %q", name)
		}
	}
	for _, input := range []Request{{Query: " "}, {Query: "q", Limit: pointer(0)}, {Query: "q", Recency: pointer(-1)}} {
		if _, err := normalizeRequest(input); err == nil {
			t.Errorf("invalid input accepted: %+v", input)
		}
	}
	for _, tc := range []struct {
		id           string
		days         int
		field, value string
	}{
		{"searxng", 30, "time_range", "month"}, {"duckduckgo", 7, "df", "w"},
	} {
		_, _, params := wireRequest(t, tc.id, Request{Query: "q", Recency: pointer(tc.days)})
		if params.Get(tc.field) != tc.value {
			t.Fatal(params)
		}
	}
}

func TestEndpointOverridesPreserveProxyAndVersionPaths(t *testing.T) {
	for _, tc := range []struct{ id, base, want string }{
		{"tavily", "https://proxy.test/prefix", "https://proxy.test/prefix/search"},
		{"exa", "https://proxy.test/search/", "https://proxy.test/search"},
		{"deepseek", "https://proxy.test/v1", "https://proxy.test/v1/tools/search"},
		{"deepseek", "https://proxy.test/v1/tools/search", "https://proxy.test/v1/tools/search"},
		{"google", "https://proxy.test/customsearch/v1", "https://proxy.test/customsearch/v1"},
		{"sogou", "https://proxy.test/prefix", "https://proxy.test/prefix/"},
	} {
		spec, _ := adapterByID(tc.id)
		got, err := endpoint(spec, tc.base)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %s %v", tc.id, got, err)
		}
	}
}

func TestProviderResponsesPreserveEveryResultAndFullText(t *testing.T) {
	text := strings.Repeat("完整正文 evidence ", 2000)
	item := map[string]any{"title": "Source", "url": "https://example.test/source", "content": text, "published_date": "2026-10-22"}
	pages := make([]any, 9)
	for i := range pages {
		pages[i] = item
	}
	encoded, _ := json.Marshal(pages)
	xmlBody := `<yandexsearch><response><results><grouping><group><doc><url>https://example.test/one</url><title>A <hlword>highlight</hlword></title><passages><passage>First <hlword>full</hlword> passage.</passage><passage>Second passage.</passage></passages></doc><doc><url>https://example.test/two</url><title>Two</title></doc></group></grouping></results></response></yandexsearch>`
	single := `{"title":"Source","url":"https://example.test/source","snippet":"Evidence"}`
	for _, tc := range []struct {
		id, body string
		count    int
		summary  string
	}{
		{"tavily", `{"results":` + string(encoded) + `}`, 9, text},
		{"exa", `{"results":[{"title":"Source","url":"https://example.test/source","text":` + string(mustJSON(text)) + `}]}`, 1, text},
		{"deepseek", `{"results":[` + single + `]}`, 1, "Evidence"},
		{"brave", `{"web":{"results":[{"title":"Source","url":"https://example.test/source","description":"Main","extra_snippets":["More","Still more"]}]}}`, 1, "Main\nMore\nStill more"},
		{"google", `{"items":[{"title":"Source","link":"https://example.test/source","snippet":"Evidence"}]}`, 1, "Evidence"},
		{"serper", `{"organic":[{"title":"Source","link":"https://example.test/source","snippet":"Evidence"}]}`, 1, "Evidence"},
		{"sogou", `{"Response":{"Pages":[` + string(mustJSON(single)) + `,` + single + `]}}`, 2, "Evidence"},
		{"searxng", `{"results":[` + single + `]}`, 1, "Evidence"},
		{"jina", `{"code":200,"data":[` + single + `]}`, 1, "Evidence"},
		{"bocha", `{"code":200,"data":{"webPages":{"value":[{"name":"Source","url":"https://example.test/source","summary":"Evidence","datePublished":"2026-10-22"}]}}}`, 1, "Evidence"},
		{"duckduckgo", `<div class="result results_links"><a href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.test%2Fsource" class="result__a"><b>Source</b> title</a><a class="result__snippet">Full <b>Evidence</b> &amp; more</a></div>`, 1, "Full Evidence & more"},
		{"yandex", `{"rawData":` + string(mustJSON(base64.StdEncoding.EncodeToString([]byte(xmlBody)))) + `}`, 2, "First full passage.\nSecond passage."},
	} {
		t.Run(tc.id, func(t *testing.T) {
			got, err := parseResults(tc.id, []byte(tc.body))
			if err != nil || len(got) != tc.count || got[0].Summary != tc.summary {
				t.Fatalf("parse mismatch: count %d, error %v", len(got), err)
			}
		})
	}
}
func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestProviderErrorsAreNotReportedAsEmptySuccess(t *testing.T) {
	for _, tc := range []struct{ id, body string }{
		{"tavily", `{"error":"invalid key"}`}, {"exa", `{}`}, {"sogou", `{"Response":{"Error":{"Code":"AuthFailure","Message":"bad credential"}}}`},
		{"jina", `{"code":401,"message":"unauthorized"}`}, {"bocha", `{"code":403,"msg":"forbidden"}`},
		{"duckduckgo", `<form id="challenge-form"><div class="anomaly-modal">Verify</div></form>`},
		{"yandex", `{"rawData":"not-base64"}`},
	} {
		if _, err := parseResults(tc.id, []byte(tc.body)); err == nil {
			t.Errorf("%s hid upstream error", tc.id)
		}
	}
}

func TestSearchHasNoResponseSizeOrResultCountCutoff(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	content := strings.Repeat("evidence ", 70000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		results := []map[string]string{}
		for i := range 8 {
			results = append(results, map[string]string{"title": fmt.Sprint(i), "url": fmt.Sprintf("https://example.test/%d", i), "content": content})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer server.Close()
	service := Service{Store: store}
	p := domain.SearchProvider{Name: "Full results", Adapter: "tavily", BaseURL: server.URL}
	if err = service.Save(context.Background(), &p, map[string]string{"api_key": "key"}); err != nil {
		t.Fatal(err)
	}
	response, err := service.Query(context.Background(), p.ID, Request{Query: "query"})
	if err != nil || len(response.Results) != 8 {
		t.Fatalf("truncated results: %d %v", len(response.Results), err)
	}
	for _, item := range response.Results {
		if item.Summary != content {
			t.Fatal("truncated search body")
		}
	}
}

func TestCredentialsStayEncryptedAndPartialUpdatesPreserveOtherFields(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	service := Service{Store: store}
	p := domain.SearchProvider{Name: "Tencent", Adapter: "sogou"}
	if err = service.Save(ctx, &p, map[string]string{"secret_id": "private-id", "secret_key": "private-key"}); err != nil {
		t.Fatal(err)
	}
	stale := p
	if strings.Contains(string(mustJSON(p)), "private-") || !slices.Equal(p.CredentialFields, []string{"secret_id", "secret_key"}) {
		t.Fatal("credentials leaked into provider document")
	}
	if err = service.Save(ctx, &p, map[string]string{"secret_key": "replacement"}); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Secret(ctx, p.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeCredentials(raw), map[string]string{"secret_id": "private-id", "secret_key": "replacement"}) {
		t.Fatal("partial update lost stored credential")
	}
	if err = service.Save(ctx, &stale, map[string]string{"secret_key": "stale"}); err == nil {
		t.Fatal("stale credential update accepted")
	}
	p.Adapter = "tavily"
	if err = service.Save(ctx, &p, nil); err == nil {
		t.Fatal("credential from another adapter was reused")
	}
	if err = service.Save(ctx, &p, map[string]string{"api_key": "new-key"}); err != nil {
		t.Fatal(err)
	}
	raw, _ = store.Secret(ctx, p.CredentialID)
	if !reflect.DeepEqual(decodeCredentials(raw), map[string]string{"api_key": "new-key"}) {
		t.Fatal("old credentials crossed adapter boundary")
	}
	p.Config = map[string]string{"api_key": "plaintext"}
	if err = service.Save(ctx, &p, nil); err == nil {
		t.Fatal("secret accepted in public configuration")
	}
}

func TestLegacySingleKeyAndUntrustedCredentialReference(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	p := domain.SearchProvider{Meta: domain.Meta{ID: "legacy"}, Name: "Legacy", Adapter: "exa", BaseURL: "https://api.exa.ai"}
	err = store.Update(ctx, func(tx *persistence.Tx) error {
		var saveErr error
		p.CredentialID, saveErr = tx.PutSecret(ctx, "legacy-key")
		if saveErr != nil {
			return saveErr
		}
		return tx.Save(ctx, "search", &p.Meta, &p)
	})
	if err != nil {
		t.Fatal(err)
	}
	credentialID := p.CredentialID
	p.CredentialID = "untrusted-reference"
	service := Service{Store: store}
	if err = service.Save(ctx, &p, nil); err != nil {
		t.Fatal(err)
	}
	if p.CredentialID != credentialID || !slices.Equal(p.CredentialFields, []string{"api_key"}) {
		t.Fatal("legacy key was not retained")
	}
	raw, _ := store.Secret(ctx, p.CredentialID)
	if decodeCredentials(raw)["api_key"] != "legacy-key" {
		t.Fatal("legacy credential unreadable")
	}
}

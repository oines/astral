package search

import "slices"

// Adapter describes both the wire protocol and the connection form. Secrets
// never belong in Config: the field definitions are also validated on save.
type Adapter struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Website         string  `json:"website"`
	BaseURL         string  `json:"base_url"`
	BaseURLRequired bool    `json:"base_url_required"`
	Fields          []Field `json:"fields"`
	path            string
}

type Field struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Secret   bool     `json:"secret"`
	Required bool     `json:"required"`
	Default  string   `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
}

var apiKeyField = Field{Name: "api_key", Label: "API Key", Secret: true, Required: true}

var adapters = []Adapter{
	{ID: "tavily", Name: "Tavily", Website: "https://tavily.com", BaseURL: "https://api.tavily.com", path: "/search", Fields: []Field{apiKeyField}},
	{ID: "exa", Name: "Exa", Website: "https://exa.ai", BaseURL: "https://api.exa.ai", path: "/search", Fields: []Field{apiKeyField}},
	{ID: "deepseek", Name: "DeepSeek Search", Website: "https://api-docs.deepseek.com/guides/search/", BaseURL: "https://api.deepseek.com", path: "/v1/tools/search", Fields: []Field{apiKeyField}},
	{ID: "brave", Name: "Brave Search", Website: "https://brave.com/search/api/", BaseURL: "https://api.search.brave.com", path: "/res/v1/web/search", Fields: []Field{apiKeyField}},
	{ID: "google", Name: "Google Custom Search", Website: "https://programmablesearchengine.google.com", BaseURL: "https://customsearch.googleapis.com", path: "/customsearch/v1", Fields: []Field{apiKeyField, {Name: "cx", Label: "Search engine ID", Required: true}}},
	{ID: "sogou", Name: "Sogou / Tencent", Website: "https://cloud.tencent.com/product/wsa", BaseURL: "https://wsa.tencentcloudapi.com", path: "/", Fields: []Field{{Name: "secret_id", Label: "SecretId", Secret: true, Required: true}, {Name: "secret_key", Label: "SecretKey", Secret: true, Required: true}}},
	{ID: "serper", Name: "Serper", Website: "https://serper.dev", BaseURL: "https://google.serper.dev", path: "/search", Fields: []Field{apiKeyField}},
	{ID: "searxng", Name: "SearXNG", Website: "https://docs.searxng.org", BaseURLRequired: true, path: "/search", Fields: []Field{{Name: "api_key", Label: "Bearer token", Secret: true}}},
	{ID: "jina", Name: "Jina Search", Website: "https://jina.ai", BaseURL: "https://s.jina.ai", path: "/search", Fields: []Field{apiKeyField}},
	{ID: "bocha", Name: "Bocha", Website: "https://open.bocha.cn", BaseURL: "https://api.bocha.cn", path: "/v1/web-search", Fields: []Field{apiKeyField}},
	{ID: "duckduckgo", Name: "DuckDuckGo", Website: "https://duckduckgo.com", BaseURL: "https://html.duckduckgo.com", path: "/html/", Fields: []Field{}},
	{ID: "yandex", Name: "Yandex Search", Website: "https://yandex.cloud/en/services/search-api", BaseURL: "https://searchapi.api.cloud.yandex.net", path: "/v2/web/search", Fields: []Field{apiKeyField, {Name: "folder_id", Label: "Folder ID", Required: true}, {Name: "search_type", Label: "Search type", Default: "SEARCH_TYPE_COM", Options: []string{"SEARCH_TYPE_COM", "SEARCH_TYPE_RU", "SEARCH_TYPE_TR", "SEARCH_TYPE_KK", "SEARCH_TYPE_BE", "SEARCH_TYPE_UZ"}}}},
}

func Adapters() []Adapter { return slices.Clone(adapters) }

func adapterByID(id string) (Adapter, bool) {
	for _, adapter := range adapters {
		if adapter.ID == id {
			return adapter, true
		}
	}
	return Adapter{}, false
}

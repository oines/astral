package search

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
	"golang.org/x/net/idna"
)

func normalizeRequest(input Request) (Request, error) {
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" {
		return input, domain.Invalid("search query must not be empty")
	}
	if input.Limit != nil && *input.Limit < 1 {
		return input, domain.Invalid("limit must be a positive integer")
	}
	if input.Recency != nil && *input.Recency < 1 {
		return input, domain.Invalid("recency must be a positive number of days")
	}
	domains := []string{}
	for _, raw := range input.Domains {
		name, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		name = strings.ToLower(name)
		if err != nil || name == "" || len(name) > 253 || net.ParseIP(name) != nil {
			return input, domain.Invalid("domains must contain domain names without a scheme, path, port, or IP address")
		}
		for _, label := range strings.Split(name, ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return input, domain.Invalid("invalid domain name: " + raw)
			}
			for _, ch := range label {
				if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
					return input, domain.Invalid("domains must contain domain names without a scheme, path, or port")
				}
			}
		}
		if !slices.Contains(domains, name) {
			domains = append(domains, name)
		}
	}
	input.Domains = domains
	return input, nil
}

// Accept a base URL (including a proxy prefix), a versioned base URL, or the
// complete endpoint. Existing Tavily/Exa base URLs keep their original meaning.
func endpoint(adapter Adapter, override string) (string, error) {
	base := strings.TrimRight(first(override, adapter.BaseURL), "/")
	if err := modelbridge.ValidateURL(base); err != nil {
		return "", err
	}
	route := strings.TrimRight(adapter.path, "/")
	if route == "" {
		return base + "/", nil
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, route) {
		if strings.HasSuffix(adapter.path, "/") {
			return base + "/", nil
		}
		return base, nil
	}
	parts := strings.Split(strings.TrimPrefix(route, "/"), "/")
	for n := len(parts) - 1; n > 0; n-- {
		prefix := "/" + strings.Join(parts[:n], "/")
		if strings.HasSuffix(path, prefix) {
			return base + "/" + strings.Join(parts[n:], "/"), nil
		}
	}
	return base + adapter.path, nil
}

func queryWithDomains(input Request, separator string) string {
	if len(input.Domains) == 0 {
		return input.Query
	}
	sites := make([]string, len(input.Domains))
	for i, name := range input.Domains {
		sites[i] = "site:" + name
	}
	if len(sites) == 1 {
		return input.Query + " " + sites[0]
	}
	return input.Query + " (" + strings.Join(sites, separator) + ")"
}

func buildRequest(ctx context.Context, adapter Adapter, provider domain.SearchProvider, keys map[string]string, input Request, now time.Time) (*http.Request, error) {
	target, err := endpoint(adapter, provider.BaseURL)
	if err != nil {
		return nil, err
	}
	from, to := "", now.UTC().Format("2006-01-02")
	if input.Recency != nil {
		// A calendar date cannot represent an unbounded number of days. This is input
		// validation, independent of any limit on returned results.
		if *input.Recency > 3652058 {
			return nil, domain.Invalid("recency is outside the supported calendar range")
		}
		start := now.UTC().AddDate(0, 0, -*input.Recency)
		if start.Year() < 1 {
			return nil, domain.Invalid("recency is outside the supported calendar range")
		}
		from = start.Format("2006-01-02")
	}
	unsupported := func(parameter string) (*http.Request, error) {
		return nil, domain.Invalid(adapter.Name + " does not support " + parameter + "; omit it or select another search provider")
	}
	body := map[string]any{}
	params := url.Values{}
	method := http.MethodPost
	authHeader, auth := "Authorization", "Bearer "+keys["api_key"]
	setLimit := func(name string) {
		if input.Limit != nil {
			body[name] = *input.Limit
		}
	}
	setCount := func(name string) {
		if input.Limit != nil {
			params.Set(name, strconv.Itoa(*input.Limit))
		}
	}
	switch adapter.ID {
	case "tavily":
		body["query"] = input.Query
		setLimit("max_results")
		if len(input.Domains) > 0 {
			body["include_domains"] = input.Domains
		}
		if from != "" {
			body["start_date"] = from
		}
	case "exa":
		authHeader, auth = "x-api-key", keys["api_key"]
		body["query"] = input.Query
		// Bare searches return metadata only. Request source excerpts without
		// fetching full page text or generating Exa-side summaries.
		body["contents"] = map[string]any{"highlights": true}
		setLimit("numResults")
		if len(input.Domains) > 0 {
			body["includeDomains"] = input.Domains
		}
		if from != "" {
			body["startPublishedDate"] = from + "T00:00:00.000Z"
		}
	case "deepseek":
		query := queryWithDomains(input, " OR ")
		if from != "" {
			query += " after:" + from
		}
		body["query"] = query
		setLimit("count")
	case "brave":
		method, authHeader, auth = http.MethodGet, "X-Subscription-Token", keys["api_key"]
		params.Set("q", queryWithDomains(input, " OR "))
		setCount("count")
		if from != "" {
			params.Set("freshness", from+"to"+to)
		}
	case "google":
		method, auth = http.MethodGet, ""
		params.Set("key", keys["api_key"])
		params.Set("cx", provider.Config["cx"])
		params.Set("q", queryWithDomains(input, " OR "))
		setCount("num")
		if input.Recency != nil {
			params.Set("dateRestrict", "d"+strconv.Itoa(*input.Recency))
		}
	case "sogou":
		auth = ""
		body["Query"] = input.Query
		setLimit("Cnt")
		if len(input.Domains) > 1 {
			return unsupported("more than one domain")
		}
		if len(input.Domains) == 1 {
			body["Site"] = input.Domains[0]
		}
		if from != "" {
			body["Freshness"] = from + "," + to
		}
	case "serper":
		authHeader, auth = "X-API-KEY", keys["api_key"]
		body["q"] = queryWithDomains(input, " OR ")
		setLimit("num")
		if input.Recency != nil {
			body["tbs"] = "qdr:d" + strconv.Itoa(*input.Recency)
		}
	case "searxng":
		if input.Limit != nil {
			return unsupported("limit")
		}
		method = http.MethodGet
		if keys["api_key"] == "" {
			auth = ""
		}
		params.Set("q", queryWithDomains(input, " OR "))
		params.Set("format", "json")
		if input.Recency != nil {
			interval := map[int]string{1: "day", 30: "month", 365: "year"}[*input.Recency]
			if interval == "" {
				return unsupported("this recency value (use 1, 30, or 365 days)")
			}
			params.Set("time_range", interval)
		}
	case "jina":
		if input.Recency != nil {
			return unsupported("recency")
		}
		method = http.MethodGet
		params.Set("q", queryWithDomains(input, " OR "))
		setCount("count")
	case "bocha":
		body["query"] = input.Query
		body["summary"] = true
		setLimit("count")
		if len(input.Domains) > 0 {
			body["include"] = strings.Join(input.Domains, ",")
		}
		if from != "" {
			body["freshness"] = from + ".." + to
		}
	case "duckduckgo":
		if input.Limit != nil {
			return unsupported("limit")
		}
		auth = ""
		params.Set("q", queryWithDomains(input, " OR "))
		if input.Recency != nil {
			interval := map[int]string{1: "d", 7: "w", 30: "m", 365: "y"}[*input.Recency]
			if interval == "" {
				return unsupported("this recency value (use 1, 7, 30, or 365 days)")
			}
			params.Set("df", interval)
		}
	case "yandex":
		auth = "Api-Key " + keys["api_key"]
		body["query"] = map[string]any{"searchType": first(provider.Config["search_type"], "SEARCH_TYPE_COM"), "queryText": queryWithDomains(input, " | ")}
		body["folderId"] = provider.Config["folder_id"]
		if input.Limit != nil {
			body["groupSpec"] = map[string]string{"groupMode": "GROUP_MODE_FLAT", "groupsOnPage": strconv.Itoa(*input.Limit)}
		}
		if input.Recency != nil {
			period := map[int]string{1: "PERIOD_DAY", 14: "PERIOD_2_WEEKS", 30: "PERIOD_MONTH"}[*input.Recency]
			if period == "" {
				return unsupported("this recency value (use 1, 14, or 30 days)")
			}
			body["period"] = period
		}
	default:
		return nil, domain.Invalid("search adapter is not supported")
	}
	var payload []byte
	contentType := "application/json"
	if adapter.ID == "duckduckgo" {
		payload = []byte(params.Encode())
		contentType = "application/x-www-form-urlencoded"
	} else if method == http.MethodGet {
		target += "?" + params.Encode()
	} else {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if method != http.MethodGet {
		req.Header.Set("Content-Type", contentType)
	}
	if auth != "" {
		req.Header.Set(authHeader, auth)
	}
	if adapter.ID == "duckduckgo" {
		req.Header.Set("Accept", "text/html")
	}
	if adapter.ID == "sogou" {
		signTencent(req, payload, keys, now.UTC())
	}
	return req, nil
}

func signTencent(req *http.Request, payload []byte, keys map[string]string, now time.Time) {
	hash := func(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
	mac := func(key []byte, value string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(value))
		return h.Sum(nil)
	}
	date, timestamp := now.Format("2006-01-02"), strconv.FormatInt(now.Unix(), 10)
	headers := "content-type:application/json\nhost:" + req.URL.Host + "\n"
	canonical := strings.Join([]string{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, headers, "content-type;host", hash(payload)}, "\n")
	scope := date + "/wsa/tc3_request"
	signed := "TC3-HMAC-SHA256\n" + timestamp + "\n" + scope + "\n" + hash([]byte(canonical))
	key := mac(mac(mac([]byte("TC3"+keys["secret_key"]), date), "wsa"), "tc3_request")
	req.Header.Set("Authorization", fmt.Sprintf("TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=content-type;host, Signature=%s", keys["secret_id"], scope, hex.EncodeToString(mac(key, signed))))
	req.Header.Set("X-TC-Action", "SearchPro")
	req.Header.Set("X-TC-Version", "2025-05-08")
	req.Header.Set("X-TC-Timestamp", timestamp)
}

package search

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

type searchItem struct {
	Title         string   `json:"title"`
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	Link          string   `json:"link"`
	Content       string   `json:"content"`
	Text          string   `json:"text"`
	Summary       string   `json:"summary"`
	Snippet       string   `json:"snippet"`
	Description   string   `json:"description"`
	Passage       string   `json:"passage"`
	Highlights    []string `json:"highlights"`
	ExtraSnippets []string `json:"extra_snippets"`
	Chunks        []string `json:"chunks"`
	PublishedDate string   `json:"publishedDate"`
	Published     string   `json:"published_date"`
	DatePublished string   `json:"datePublished"`
	PublishedTime string   `json:"publishedTime"`
	Date          string   `json:"date"`
	Age           string   `json:"age"`
	Author        string   `json:"author"`
}

func (item searchItem) result() Result {
	summary := firstNonBlankContent(item.Content, item.Text, item.Summary, item.Passage, item.Snippet, item.Description, strings.Join(item.Highlights, "\n"), strings.Join(item.Chunks, "\n"))
	for _, snippet := range item.ExtraSnippets {
		if strings.TrimSpace(snippet) == "" {
			continue
		}
		if summary != "" {
			summary += "\n"
		}
		summary += snippet
	}
	return Result{Title: first(item.Title, item.Name), URL: first(item.URL, item.Link), Summary: summary,
		PublishedAt: first(item.PublishedDate, item.Published, item.DatePublished, item.PublishedTime, item.Date, item.Age), Author: item.Author}
}

// Check for content without changing the source text's whitespace or length.
func firstNonBlankContent(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func decodeSearchItems(raw json.RawMessage) ([]searchItem, error) {
	var objects []*searchItem
	if json.Unmarshal(raw, &objects) != nil || objects == nil {
		return nil, errors.New("missing or invalid search results")
	}
	items := make([]searchItem, len(objects))
	for i, object := range objects {
		if object == nil {
			return nil, errors.New("invalid item in search results")
		}
		items[i] = *object
	}
	return items, nil
}

func validJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func errorMessage(data []byte) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil {
		return ""
	}
	for _, key := range []string{"error", "Error", "message", "Message", "msg", "detail"} {
		var message string
		if json.Unmarshal(object[key], &message) == nil && message != "" {
			return message
		}
		if nested := errorMessage(object[key]); nested != "" {
			return nested
		}
	}
	return ""
}

func parseResults(adapter string, data []byte) ([]Result, error) {
	if adapter == "duckduckgo" {
		return parseDuckDuckGo(data)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return nil, errors.New("invalid search response")
	}
	if raw := object["error"]; len(raw) > 0 && string(raw) != "null" {
		return nil, errors.New(first(errorMessage(data), "search provider reported an error"))
	}
	var items []searchItem
	var raw json.RawMessage
	switch adapter {
	case "tavily", "exa", "deepseek", "searxng":
		raw = object["results"]
	case "brave":
		var web struct {
			Results json.RawMessage `json:"results"`
		}
		if len(object["web"]) == 0 || bytes.Equal(bytes.TrimSpace(object["web"]), []byte("null")) {
			var query struct {
				Original string `json:"original"`
			}
			var responseType string
			if validJSONObject(object["query"]) && json.Unmarshal(object["query"], &query) == nil &&
				query.Original != "" && json.Unmarshal(object["type"], &responseType) == nil && responseType == "search" {
				return []Result{}, nil
			}
			return nil, errors.New("missing web results")
		}
		if !validJSONObject(object["web"]) || json.Unmarshal(object["web"], &web) != nil {
			return nil, errors.New("invalid web results")
		}
		raw = web.Results
	case "google":
		raw = object["items"]
		if len(raw) == 0 {
			var info struct {
				TotalResults string `json:"totalResults"`
			}
			if validJSONObject(object["searchInformation"]) && json.Unmarshal(object["searchInformation"], &info) == nil && info.TotalResults == "0" {
				return []Result{}, nil
			}
			return nil, errors.New("missing search results")
		}
	case "serper":
		if len(object["statusCode"]) > 0 {
			var code *int
			if json.Unmarshal(object["statusCode"], &code) != nil || code == nil {
				return nil, errors.New("invalid search status code")
			}
			if *code < 200 || *code >= 300 {
				return nil, errors.New(first(errorMessage(data), fmt.Sprintf("search failed with status code %d", *code)))
			}
		}
		raw = object["organic"]
	case "jina":
		for _, field := range []string{"code", "status"} {
			if len(object[field]) == 0 {
				continue
			}
			var value *int
			if json.Unmarshal(object[field], &value) != nil || value == nil {
				return nil, fmt.Errorf("invalid search %s", field)
			}
			success := *value == 200
			if field == "status" {
				success = *value/100 == 200
			}
			if !success {
				return nil, errors.New(first(errorMessage(data), fmt.Sprintf("search failed with %s %d", field, *value)))
			}
		}
		raw = object["data"]
	case "bocha":
		var wire struct {
			Code *int `json:"code"`
			Data *struct {
				WebPages struct {
					Value json.RawMessage `json:"value"`
				} `json:"webPages"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &wire) != nil {
			return nil, errors.New("invalid search response")
		}
		if len(object["code"]) > 0 && wire.Code == nil {
			return nil, errors.New("invalid search code")
		}
		if wire.Code != nil && *wire.Code != 200 {
			return nil, errors.New(first(errorMessage(data), fmt.Sprintf("search failed with code %d", *wire.Code)))
		}
		if wire.Data == nil {
			return nil, errors.New("missing search data")
		}
		if value := wire.Data.WebPages.Value; len(value) > 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			var err error
			items, err = decodeSearchItems(value)
			if err != nil {
				return nil, err
			}
		}
	case "sogou":
		var wire struct {
			Response *struct {
				Error json.RawMessage   `json:"Error"`
				Pages []json.RawMessage `json:"Pages"`
			} `json:"Response"`
		}
		if json.Unmarshal(data, &wire) != nil || wire.Response == nil {
			return nil, errors.New("invalid search response")
		}
		if len(wire.Response.Error) > 0 && string(wire.Response.Error) != "null" {
			return nil, errors.New(first(errorMessage(wire.Response.Error), "search provider reported an error"))
		}
		for _, page := range wire.Response.Pages {
			var encoded string
			if json.Unmarshal(page, &encoded) == nil {
				page = []byte(encoded)
			}
			var item searchItem
			if json.Unmarshal(page, &item) != nil {
				return nil, errors.New("invalid page in search response")
			}
			items = append(items, item)
		}
	case "yandex":
		var encoded string
		if json.Unmarshal(object["rawData"], &encoded) != nil || encoded == "" {
			return nil, errors.New("missing XML search data")
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, errors.New("invalid encoded search data")
		}
		return parseYandex(decoded)
	default:
		return nil, errors.New("search adapter is not supported")
	}
	switch adapter {
	case "tavily", "exa", "deepseek", "searxng", "brave", "google", "serper", "jina":
		var err error
		items, err = decodeSearchItems(raw)
		if err != nil {
			return nil, err
		}
	}
	results := make([]Result, 0, len(items))
	for _, item := range items {
		result := item.result()
		if validSource(result.URL) {
			results = append(results, result)
		}
	}
	return results, nil
}

func validSource(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}

func plainText(raw string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(raw))
	var out strings.Builder
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return strings.TrimSpace(out.String())
		case html.TextToken:
			out.Write(tokenizer.Text())
		}
	}
}

func parseYandex(data []byte) ([]Result, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	results := []Result{}
	seenResponse, seenResults, noResults := false, false, false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("invalid XML search response")
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "response":
			seenResponse = true
		case "results":
			seenResults = true
		case "error":
			var detail struct {
				Code string `xml:"code,attr"`
				Text string `xml:",chardata"`
			}
			if decoder.DecodeElement(&detail, &start) != nil {
				return nil, errors.New("invalid XML search error")
			}
			// Yandex's documented code 15 means no matching results.
			if detail.Code == "15" {
				noResults = true
				continue
			}
			return nil, fmt.Errorf("search error %s: %s", detail.Code, detail.Text)
		case "doc":
			var doc struct {
				Title struct {
					Text string `xml:",innerxml"`
				} `xml:"title"`
				URL      string `xml:"url"`
				ModTime  string `xml:"modtime"`
				Passages []struct {
					Text string `xml:",innerxml"`
				} `xml:"passages>passage"`
			}
			if decoder.DecodeElement(&doc, &start) != nil {
				return nil, errors.New("invalid XML search document")
			}
			passages := make([]string, len(doc.Passages))
			for i, passage := range doc.Passages {
				passages[i] = plainText(passage.Text)
			}
			if validSource(doc.URL) {
				results = append(results, Result{Title: plainText(doc.Title.Text), URL: doc.URL, Summary: strings.Join(passages, "\n"), PublishedAt: doc.ModTime})
			}
		}
	}
	if !seenResponse || !seenResults && !noResults {
		return nil, errors.New("missing XML search response")
	}
	return results, nil
}

func parseDuckDuckGo(data []byte) ([]Result, error) {
	document, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid HTML search response")
	}
	results := []Result{}
	challenged, empty := false, false
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		for _, attribute := range node.Attr {
			if (attribute.Key == "id" || attribute.Key == "class") && strings.Contains(attribute.Val, "anomaly") {
				challenged = true
			}
		}
		if hasClass(node, "no-results") || hasClass(node, "result--no-result") {
			empty = true
		}
		if hasClass(node, "result") && !hasClass(node, "result--ad") {
			link := findClass(node, "result__a")
			snippet := findClass(node, "result__snippet")
			if link != nil {
				href := ""
				for _, attribute := range link.Attr {
					if attribute.Key == "href" {
						href = attribute.Val
					}
				}
				parsed, parseErr := url.Parse(href)
				if parseErr == nil {
					if target := parsed.Query().Get("uddg"); target != "" {
						href = target
					} else if parsed.Scheme == "" && parsed.Host != "" {
						href = "https:" + href
					}
				}
				if validSource(href) {
					results = append(results, Result{Title: nodeText(link), URL: href, Summary: nodeText(snippet)})
				}
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	if challenged {
		return nil, errors.New("DuckDuckGo requires a browser verification challenge; select another search provider")
	}
	if len(results) == 0 && !empty {
		return nil, errors.New("unrecognized DuckDuckGo search page")
	}
	return results, nil
}

func hasClass(node *html.Node, name string) bool {
	for _, attribute := range node.Attr {
		if attribute.Key == "class" {
			for _, class := range strings.Fields(attribute.Val) {
				if class == name {
					return true
				}
			}
		}
	}
	return false
}

func findClass(node *html.Node, name string) *html.Node {
	if hasClass(node, name) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findClass(child, name); found != nil {
			return found
		}
	}
	return nil
}

func nodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var result strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.TextNode {
			result.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return strings.TrimSpace(result.String())
}

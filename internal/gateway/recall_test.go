package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/recall"
	"github.com/oines/astral/internal/testkit"
)

func TestRecallThroughComputerGatewayWithoutUsableModel(t *testing.T) {
	long := strings.Repeat("背景🙂", 1500) + "电脑权限 first decision" + strings.Repeat("背景", 500) + "电脑权限 second decision"
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text(long) })
	if _, err := p.Harness.Submit(p.Context, p.Session.ID, harness.InputRequest{ID: "first", Text: "start"}); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, p.Session.ID)
	other := p.Agent
	other.Meta = domain.Meta{}
	if err := p.Harness.SaveAgent(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	foreign, err := p.Harness.CreateSession(p.Context, other.ID, "private")
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "archive")
	if err != nil {
		t.Fatal(err)
	}
	second.Archived = true
	if err = p.Store.Save(p.Context, "sessions", &second.Meta, &second); err != nil {
		t.Fatal(err)
	}
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	selected, selectionErr := p.Harness.Core(p.Context, p.Session.ID)
	if selectionErr != nil {
		t.Fatal(selectionErr)
	}
	if selectionErr = selected.SelectModel(p.Context, core.ModelSelection{ID: "removed-model"}); selectionErr != nil {
		t.Fatal(selectionErr)
	}
	if err = p.Harness.Close(p.Context); err != nil {
		t.Fatal(err)
	}
	if err = p.Store.Get(p.Context, "sessions", p.Session.ID, &p.Session); err != nil {
		t.Fatal(err)
	}
	if err = p.Store.Save(p.Context, "sessions", &p.Session.Meta, &p.Session); err != nil {
		t.Fatal(err)
	}
	p.Harness = harness.New(p.Store, p.CoreStore, p.Harness.Models, p.Hub)
	gateway := New(p.Harness, channels.New(p.Store, p.Harness))
	p.Hub.Gateway = gateway.Handle
	if _, err = p.Harness.Core(p.Context, p.Session.ID); err != nil {
		t.Fatalf("cold history should open: %v", err)
	}
	if _, err = p.Harness.InspectContext(p.Context, p.Session.ID); err == nil {
		t.Fatal("fixture model should be unusable for requests")
	}

	before, err := p.CoreStore.LoadState(p.Context, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	requests := p.Requests.Load()
	client := testkit.ComputerAPI(t, p, binding)
	call := func(method, path string, input, output any, status int) {
		t.Helper()
		var body []byte
		if input != nil {
			body = testkit.JSON(input)
		}
		req, err := http.NewRequestWithContext(p.Context, method, binding.APIURL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if input != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if output == nil {
			output = new(any)
		}
		if err = json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("%s %s: status=%d body=%+v", method, path, response.StatusCode, output)
		}
	}
	var catalog recall.SessionPage
	call("GET", "/v1/sessions?limit=1", nil, &catalog, 200)
	ids := []string{}
	for {
		for _, session := range catalog.Items {
			ids = append(ids, session.ID)
		}
		if catalog.NextCursor == "" {
			break
		}
		var next recall.SessionPage
		call("GET", "/v1/sessions?cursor="+url.QueryEscape(catalog.NextCursor), nil, &next, 200)
		catalog = next
	}
	if len(ids) != 2 || !slices.Contains(ids, p.Session.ID) || !slices.Contains(ids, second.ID) {
		t.Fatalf("catalog leaked or omitted Sessions: %v", ids)
	}
	var hits recall.SearchPage
	call("POST", "/v1/sessions/search", recall.SearchRequest{Query: "电脑", Limit: 1, ContextChars: 80}, &hits, 200)
	if len(hits.Hits) != 1 || hits.Hits[0].Start < 4000 || hits.NextCursor == "" {
		t.Fatalf("deep match missing: %+v", hits)
	}
	var more recall.SearchPage
	call("POST", "/v1/sessions/search", map[string]any{"cursor": hits.NextCursor}, &more, 200)
	if len(more.Hits) != 1 || more.NextCursor != "" || more.Hits[0].Start <= hits.Hits[0].Start {
		t.Fatalf("passage cursor: %+v", more)
	}
	var around recall.HistoryPage
	call("GET", hits.Hits[0].ReadURL+"&before=0&after=0&length=100", nil, &around, 200)
	if len(around.Items) != 1 || !strings.Contains(around.Items[0].Text, "first decision") {
		t.Fatalf("anchored read: %+v", around)
	}
	var filtered recall.SessionPage
	call("GET", "/v1/sessions?archived=true", nil, &filtered, 200)
	if len(filtered.Items) != 1 || filtered.Items[0].ID != second.ID {
		t.Fatalf("archive filter: %+v", filtered)
	}
	call("POST", "/v1/sessions/search", recall.SearchRequest{Query: "电脑", SessionIDs: []string{foreign.ID}}, nil, 403)
	call("GET", "/v1/sessions/"+foreign.ID+"/history", nil, nil, 403)
	call("POST", "/v1/sessions/search", map[string]any{"query": "电脑", "agent_id": other.ID}, nil, 400)
	for _, path := range []string{"/v1/sessions?limit=bad", "/v1/sessions?archived=1", "/v1/sessions?limit=1&limit=2", "/v1/sessions/" + p.Session.ID + "/history?after=2", "/v1/sessions/" + p.Session.ID + "/history?trace=true"} {
		call("GET", path, nil, nil, 400)
	}
	var capabilities Capabilities
	call("GET", "/v1/capabilities", nil, &capabilities, 200)
	var historyURL string
	for _, capability := range capabilities.Capabilities {
		if capability.ID == "history" {
			historyURL = capability.ReadURL
		}
	}
	if historyURL == "" {
		t.Fatal("recall is not discoverable")
	}
	var historyDocs CapabilityDocument
	call("GET", historyURL, nil, &historyDocs, 200)
	if !strings.Contains(strings.Join(historyDocs.Notes, " "), "read_url") || historyDocs.OpenAPI["paths"].(map[string]any)["/v1/sessions/search"] == nil {
		t.Fatal("history discovery does not include instructions and actual schemas")
	}
	var contract map[string]any
	call("GET", "/v1/openapi.json", nil, &contract, 200)
	paths := contract["paths"].(map[string]any)
	for _, path := range []string{"/v1/sessions", "/v1/sessions/search", "/v1/sessions/{id}/history"} {
		if paths[path] == nil {
			t.Fatalf("missing live contract: %s", path)
		}
	}
	// The Owner's raw history API also reads the store without opening a model.
	if page, err := History(p.Context, p.Harness, p.Session.ID, "", 20, false, false); err != nil || len(page.Entries) == 0 {
		t.Fatalf("raw history depends on model: %+v %v", page, err)
	}
	after, err := p.CoreStore.LoadState(p.Context, p.Session.ID)
	if err != nil || !reflect.DeepEqual(before, after) || p.Requests.Load() != requests {
		t.Fatal("recall changed execution state or invoked the model")
	}
}

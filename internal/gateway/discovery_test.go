package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/testkit"
)

func TestDiscoveryEntryLeadsToSelfContainedLiveContracts(t *testing.T) {
	s := New(nil, nil)
	read := func(path string, target any, status int) {
		t.Helper()
		response := httptest.NewRecorder()
		s.mux.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != status {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		if target != nil {
			if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
				t.Fatal(err)
			}
		}
	}
	var index Capabilities
	read("/v1/capabilities", &index, 200)
	if len(index.Capabilities) != 4 || index.Authentication == "" || index.Scope == "" || index.OpenAPIURL == "" {
		t.Fatalf("entry is missing discovery instructions: %+v", index)
	}
	var full map[string]any
	read(index.OpenAPIURL, &full, 200)
	checkSecurity := func(document map[string]any) {
		t.Helper()
		schemes := document["components"].(map[string]any)["securitySchemes"].(map[string]any)
		scheme := schemes["command"].(map[string]any)
		if scheme["type"] != "http" || scheme["scheme"] != "bearer" || !strings.Contains(scheme["description"].(string), "ASTRAL_API_TOKEN") {
			t.Fatalf("command credential definition is missing: %+v", scheme)
		}
		for path, raw := range document["paths"].(map[string]any) {
			for method, rawOperation := range raw.(map[string]any) {
				operation := rawOperation.(map[string]any)
				security, _ := json.Marshal(operation["security"])
				if string(security) != `[{"command":[]}]` {
					t.Errorf("%s %s does not require its command credential: %s", method, path, security)
				}
			}
		}
	}
	checkSecurity(full)
	covered := map[string]bool{}
	for _, capability := range index.Capabilities {
		var doc CapabilityDocument
		read(capability.ReadURL, &doc, 200)
		if doc.ID != capability.ID || len(doc.Notes) == 0 || len(doc.Examples) == 0 {
			t.Fatalf("missing instructions for %s", capability.ID)
		}
		checkSecurity(doc.OpenAPI)
		if !strings.Contains(strings.Join(doc.Notes, " "), "ASTRAL_API_TOKEN") {
			t.Fatalf("%s has no command authentication instructions", capability.ID)
		}
		paths := doc.OpenAPI["paths"].(map[string]any)
		for path, raw := range paths {
			for method, operation := range raw.(map[string]any) {
				covered[method+" "+path] = true
				actual := full["paths"].(map[string]any)[path].(map[string]any)[method]
				left, _ := json.Marshal(operation)
				right, _ := json.Marshal(actual)
				if string(left) != string(right) {
					t.Fatalf("%s %s discovery diverges from the actual route", method, path)
				}
			}
		}
		for _, example := range doc.Examples {
			path := strings.SplitN(example.Path, "?", 2)[0]
			if paths[path] == nil || paths[path].(map[string]any)[strings.ToLower(example.Method)] == nil {
				t.Fatalf("example has no matching schema: %+v", example)
			}
		}
		schemas := doc.OpenAPI["components"].(map[string]any)["schemas"].(map[string]any)
		var checkRefs func(any)
		checkRefs = func(value any) {
			switch value := value.(type) {
			case map[string]any:
				if ref, ok := value["$ref"].(string); ok {
					name := strings.TrimPrefix(ref, "#/components/schemas/")
					if schemas[name] == nil {
						t.Errorf("%s has an unresolved schema reference: %s", doc.ID, ref)
					}
				}
				for _, child := range value {
					checkRefs(child)
				}
			case []any:
				for _, child := range value {
					checkRefs(child)
				}
			}
		}
		checkRefs(doc.OpenAPI)
	}
	for path, raw := range full["paths"].(map[string]any) {
		if strings.HasPrefix(path, "/v1/capabilities") {
			continue
		}
		for method := range raw.(map[string]any) {
			if !covered[method+" "+path] {
				t.Errorf("registered operation is undiscoverable: %s %s", method, path)
			}
		}
	}
	for _, path := range []string{"/v1/im/messages", "/v1/im/files"} {
		operation := full["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)
		found := false
		for _, raw := range operation["parameters"].([]any) {
			p := raw.(map[string]any)
			found = found || p["in"] == "header" && p["name"] == "Idempotency-Key" && p["required"] == true
		}
		if !found {
			t.Errorf("send schema omitted its mandatory header: %s", path)
		}
	}
	read("/v1/capabilities/missing", nil, 404)
}

func TestComputerDiscoveryRequiresCommandCredentialAndMatchingListener(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	api := New(p.Harness, channels.New(p.Store, p.Harness))
	p.Hub.Gateway = api.Handle
	first, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "other")
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Hub.Bind(p.Context, p.Agent.ID, other.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := testkit.ComputerAPI(t, p, first)
	check := func(client *http.Client, address string, expected int) {
		t.Helper()
		request, err := http.NewRequestWithContext(p.Context, http.MethodGet, address, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != expected {
			t.Fatalf("%s: status=%d body=%s", address, response.StatusCode, body)
		}
	}
	for _, path := range []string{"/v1/capabilities", "/v1/capabilities/history", "/v1/openapi.json", "/v1/context"} {
		check(http.DefaultClient, first.APIURL+path, http.StatusUnauthorized)
		check(client, first.APIURL+path, http.StatusOK)
		check(client, second.APIURL+path, http.StatusUnauthorized)
	}
	check(testkit.ComputerAPI(t, p, second), second.APIURL+"/v1/context", http.StatusOK)
}

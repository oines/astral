package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
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
	covered := map[string]bool{}
	for _, capability := range index.Capabilities {
		var doc CapabilityDocument
		read(capability.ReadURL, &doc, 200)
		if doc.ID != capability.ID || len(doc.Notes) == 0 || len(doc.Examples) == 0 {
			t.Fatalf("missing instructions for %s", capability.ID)
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

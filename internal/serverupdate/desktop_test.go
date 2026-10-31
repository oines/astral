package serverupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestDesktopUpdateClientUsesOnlyPrivateFixedOperations(t *testing.T) {
	token := strings.Repeat("a", 64)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Origin") != "" || r.ContentLength > 0 {
			t.Error("private update request changed credentials, origin or accepted browser input")
		}
		paths = append(paths, r.Method+" "+r.URL.Path)
		_ = json.NewEncoder(w).Encode(State{Phase: "checking", Manager: "desktop", CurrentVersion: "0.1.1", OperationID: "op1", OperationState: "running"})
	}))
	defer server.Close()
	updater, err := NewDesktop(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []func(context.Context) (State, error){updater.State, updater.Check, updater.Apply} {
		state, err := call(context.Background())
		if err != nil || state.OperationID != "op1" {
			t.Fatalf("invalid update state: %+v %v", state, err)
		}
	}
	if strings.Join(paths, ",") != "GET /state,POST /check,POST /apply" {
		t.Fatalf("unexpected private operations: %v", paths)
	}
}
func TestDesktopUpdateClientRejectsArbitraryTargetsAndCredentials(t *testing.T) {
	token := strings.Repeat("a", 64)
	for _, endpoint := range []string{"https://127.0.0.1:1234", "http://localhost:1234", "http://example.com:1234", "http://127.0.0.2:1234", "http://127.0.0.1", "http://user@127.0.0.1:1234", "http://127.0.0.1:1234/state", "http://127.0.0.1:1234?token=secret", "http://127.0.0.1:1234#fragment"} {
		if _, err := NewDesktop(endpoint, token); err == nil {
			t.Errorf("accepted private update endpoint %q", endpoint)
		}
	}
	for _, key := range []string{"", strings.Repeat("a", 63), strings.Repeat("x", 64), strings.Repeat("a", 66)} {
		if _, err := NewDesktop("http://127.0.0.1:1234", key); err == nil {
			t.Error("accepted malformed private secret")
		}
	}
}
func TestDesktopUpdateClientDoesNotFollowRedirectsOrExposePrivateResponses(t *testing.T) {
	contacted := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted = true }))
	defer target.Close()
	for _, scenario := range []string{"redirect", "large", "invalid", "trailing", "wrong-manager"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				case "large":
					_, _ = w.Write([]byte(strings.Repeat("private-body", 13000)))
				case "invalid":
					_, _ = w.Write([]byte("private-body"))
				case "trailing":
					_, _ = w.Write([]byte(`{"manager":"desktop","phase":"idle","current_version":"1.0.0"} private-body`))
				case "wrong-manager":
					_, _ = w.Write([]byte(`{"manager":"manual","phase":"idle","current_version":"1.0.0"}`))
				}
			}))
			defer server.Close()
			controller, err := NewDesktop(server.URL, strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			_, err = controller.State(context.Background())
			if err == nil || strings.Contains(err.Error(), "private-body") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("invalid or secret-bearing error: %v", err)
			}
		})
	}
	if contacted {
		t.Fatal("private update redirect escaped the trusted endpoint")
	}
}

func TestDesktopCacheCleanupRelayKeepsPathsPrivateAndStableIdentity(t *testing.T) {
	token := "desktop:" + strings.Repeat("c", 64)
	var requests []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("Origin") != "" {
			t.Error("cleanup relay lost private authentication")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /storage/cache":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []CacheItem{{Token: token, Kind: "update_cache", Name: "Astral-0.1.6-mac-arm64.zip", Bytes: 2048}}})
		case "POST /storage/cache/clean":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			requests = append(requests, body)
			_ = json.NewEncoder(w).Encode(map[string]int64{"removed_bytes": 2048})
		default:
			t.Errorf("arbitrary cleanup route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	controller, err := NewDesktop(server.URL, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	cleanup := controller.(CacheCleanup)
	items, err := cleanup.CacheInventory(context.Background())
	if err != nil || len(items) != 1 || items[0].Token != token {
		t.Fatalf("inventory: %+v %v", items, err)
	}
	for range 2 {
		removed, err := cleanup.CleanCache(context.Background(), "stable-operation", token)
		if err != nil || removed != 2048 {
			t.Fatalf("cleanup: %d %v", removed, err)
		}
	}
	if !reflect.DeepEqual(requests, []map[string]string{{"operation_id": "stable-operation", "token": token}, {"operation_id": "stable-operation", "token": token}}) {
		t.Fatalf("cleanup exposed a path or changed identity: %+v", requests)
	}
	if _, err = cleanup.CleanCache(context.Background(), "stable-operation", "/private/file"); err == nil {
		t.Fatal("accepted a path instead of a cleanup token")
	}
}

func TestDesktopCacheRelayRejectsMalformedInventoryAndOutcome(t *testing.T) {
	for _, body := range []string{`{}`, `{"items":[{"token":"desktop:bad","kind":"update_cache","name":"installer.exe","bytes":1}]}`, `{"items":[{"token":"desktop:` + strings.Repeat("a", 64) + `","kind":"update_cache","name":"/private/file","bytes":1}]}`, `{"removed_bytes":-1}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			controller, err := NewDesktop(server.URL, strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			cleanup := controller.(CacheCleanup)
			if _, err = cleanup.CacheInventory(context.Background()); err == nil {
				t.Fatal("accepted malformed inventory")
			}
			if _, err = cleanup.CleanCache(context.Background(), "operation", "desktop:"+strings.Repeat("a", 64)); err == nil {
				t.Fatal("accepted malformed cleanup result")
			}
		})
	}
}

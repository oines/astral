package serverupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

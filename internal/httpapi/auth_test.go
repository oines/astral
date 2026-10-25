package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/persistence"
)

func authServer(t *testing.T) (*auth.Service, *httptest.Server) {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	service, err := auth.New(store, &auth.Config{DesktopToken: "private-runtime-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Setup(context.Background(), "owner", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	api := New(Config{Auth: service})
	mux := http.NewServeMux()
	mux.Handle("/auth/", api.AuthHandler())
	mux.Handle("/api/v1/", api)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return service, server
}
func browser(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := *server.Client()
	client.Jar = jar
	return &client
}
func authRequest(t *testing.T, c *http.Client, server *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	r, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func signIn(t *testing.T, c *http.Client, server *httptest.Server) {
	t.Helper()
	response := authRequest(t, c, server, "POST", "/auth/login", `{"username":"owner","password":"correct-horse-battery"}`)
	defer response.Body.Close()
	if response.StatusCode != 200 {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("login %d %s", response.StatusCode, raw)
	}
}
func TestPublicStatusLegacyCredentialRemovalAndServerLogout(t *testing.T) {
	_, server := authServer(t)
	client := browser(t, server)
	response := authRequest(t, client, server, "GET", "/auth/status", "")
	var status auth.Status
	_ = json.NewDecoder(response.Body).Decode(&status)
	response.Body.Close()
	if response.StatusCode != 200 || !status.Configured || status.Authenticated {
		t.Fatalf("public status %+v %d", status, response.StatusCode)
	}
	signIn(t, client, server)
	response = authRequest(t, client, server, "GET", "/api/v1/auth", "")
	var view auth.View
	_ = json.NewDecoder(response.Body).Decode(&view)
	response.Body.Close()
	if len(view.Sessions) != 1 || view.CurrentSessionID == "" || view.Desktop {
		t.Fatalf("browser not independent %+v", view)
	}
	r, _ := http.NewRequest("GET", server.URL+"/api/v1/auth", nil)
	r.Header.Set("Authorization", "Bearer private-runtime-key")
	r.AddCookie(&http.Cookie{Name: "astral_owner", Value: "private-runtime-key"})
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("legacy Owner credential accepted")
	}
	response = authRequest(t, client, server, "POST", "/auth/session", `{"token":"anything"}`)
	response.Body.Close()
	if response.StatusCode == 200 {
		t.Fatal("legacy login endpoint retained")
	}
	response = authRequest(t, client, server, "DELETE", "/auth/session", "")
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	response = authRequest(t, client, server, "GET", "/api/v1/auth", "")
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("logout only deleted a cookie")
	}
}
func TestCrossOriginLoginLogoutAndFetchMetadataAreRejected(t *testing.T) {
	_, server := authServer(t)
	client := browser(t, server)
	signIn(t, client, server)
	for _, path := range []string{"/auth/session", "/api/v1/auth/sessions"} {
		r, _ := http.NewRequest("DELETE", server.URL+path, nil)
		r.Header.Set("Origin", "https://evil.example")
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatalf("cross-origin %s returned %d", path, response.StatusCode)
		}
	}
	r, _ := http.NewRequest("POST", server.URL+"/auth/login", strings.NewReader(`{"username":"owner","password":"correct-horse-battery"}`))
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-site metadata ignored")
	}
	response = authRequest(t, client, server, "GET", "/auth/status", "")
	var status auth.Status
	_ = json.NewDecoder(response.Body).Decode(&status)
	response.Body.Close()
	if !status.Authenticated {
		t.Fatal("cross-site logout revoked session")
	}
}
func TestRevocationTerminatesOpenAuthSSEAndRejectsFurtherRequests(t *testing.T) {
	_, server := authServer(t)
	owner, other := browser(t, server), browser(t, server)
	signIn(t, owner, server)
	signIn(t, other, server)
	response := authRequest(t, other, server, "GET", "/api/v1/auth", "")
	var view auth.View
	_ = json.NewDecoder(response.Body).Decode(&view)
	response.Body.Close()
	stream := authRequest(t, other, server, "GET", "/api/v1/auth/events", "")
	defer stream.Body.Close()
	scanner := bufio.NewScanner(stream.Body)
	if !scanner.Scan() || scanner.Text() != "event: ready" {
		t.Fatal("auth SSE did not start")
	}
	done := make(chan struct{})
	go func() {
		for scanner.Scan() {
		}
		close(done)
	}()
	response = authRequest(t, owner, server, "DELETE", "/api/v1/auth/sessions/"+view.CurrentSessionID, "")
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("revoke status %d", response.StatusCode)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("revoked browser kept its open auth connection")
	}
	response = authRequest(t, other, server, "GET", "/api/v1/auth", "")
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("revoked browser kept API access")
	}
	response = authRequest(t, owner, server, "GET", "/api/v1/auth", "")
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("revoking another session affected current owner")
	}
}

func TestPasswordReauthenticationSetsRotatedCookieOnSameSession(t *testing.T) {
	_, server := authServer(t)
	client := browser(t, server)
	signIn(t, client, server)
	response := authRequest(t, client, server, "GET", "/api/v1/auth", "")
	var before auth.View
	_ = json.NewDecoder(response.Body).Decode(&before)
	response.Body.Close()
	parsed, _ := url.Parse(server.URL)
	oldCookies := client.Jar.Cookies(parsed)
	oldValue := ""
	for _, cookie := range oldCookies {
		if cookie.Name == auth.CookieName {
			oldValue = cookie.Value
		}
	}
	response = authRequest(t, client, server, "POST", "/api/v1/auth/reauth", `{"password":"correct-horse-battery"}`)
	response.Body.Close()
	newValue := ""
	for _, cookie := range response.Cookies() {
		if cookie.Name == auth.CookieName {
			newValue = cookie.Value
		}
	}
	if response.StatusCode != 200 || newValue == "" || newValue == oldValue {
		t.Fatalf("password reauth did not set a new cookie: %d %+v", response.StatusCode, response.Cookies())
	}
	request, _ := http.NewRequest("GET", server.URL+"/api/v1/auth", nil)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: oldValue})
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("old cookie accepted after HTTP reauth")
	}
	response = authRequest(t, client, server, "GET", "/api/v1/auth", "")
	var after auth.View
	_ = json.NewDecoder(response.Body).Decode(&after)
	response.Body.Close()
	if response.StatusCode != 200 || after.CurrentSessionID != before.CurrentSessionID {
		t.Fatalf("rotated cookie did not retain same session: %+v", after)
	}
}

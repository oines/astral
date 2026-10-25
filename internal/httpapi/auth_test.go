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
	"github.com/pquerna/otp/totp"
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

func TestHTTPSAndHTTPLoginsUseIndependentCookiesAndLogoutOnlyCurrentProtocol(t *testing.T) {
	service, plain := authServer(t)
	api := New(Config{Auth: service})
	mux := http.NewServeMux()
	mux.Handle("/auth/", api.AuthHandler())
	mux.Handle("/api/v1/", api)
	secure := httptest.NewTLSServer(mux)
	t.Cleanup(secure.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := *secure.Client()
	client.Jar = jar
	login := func(server *httptest.Server, expectedName string) {
		t.Helper()
		response := authRequest(t, &client, server, "POST", "/auth/login", `{"username":"owner","password":"correct-horse-battery"}`)
		defer response.Body.Close()
		cookies := response.Cookies()
		if response.StatusCode != 200 || len(cookies) != 1 || cookies[0].Name != expectedName || cookies[0].Path != "/" || cookies[0].Domain != "" {
			t.Fatalf("login did not isolate protocol cookie: %d %+v", response.StatusCode, cookies)
		}
		if expectedName == auth.SecureCookieName && !cookies[0].Secure {
			t.Fatal("__Host cookie missing Secure")
		}
	}
	view := func(server *httptest.Server) auth.View {
		t.Helper()
		response := authRequest(t, &client, server, "GET", "/api/v1/auth", "")
		defer response.Body.Close()
		var value auth.View
		if response.StatusCode != 200 {
			t.Fatalf("protocol login is not usable: %d", response.StatusCode)
		}
		if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	logout := func(server *httptest.Server, expectedName string, secureFlag bool) {
		t.Helper()
		response := authRequest(t, &client, server, "DELETE", "/auth/session", "")
		response.Body.Close()
		cookies := response.Cookies()
		if response.StatusCode != 200 || len(cookies) != 1 || cookies[0].Name != expectedName || cookies[0].Secure != secureFlag || cookies[0].Path != "/" || cookies[0].Domain != "" || cookies[0].MaxAge >= 0 {
			t.Fatalf("logout cleared wrong protocol cookie: %d %+v", response.StatusCode, cookies)
		}
	}
	login(secure, auth.SecureCookieName)
	secureSession := view(secure).CurrentSessionID
	login(plain, auth.CookieName)
	plainSession := view(plain).CurrentSessionID
	if plainSession == secureSession {
		t.Fatal("protocol logins unexpectedly shared one session")
	}
	if view(secure).CurrentSessionID != secureSession {
		t.Fatal("HTTP login replaced HTTPS cookie on same host")
	}
	logout(plain, auth.CookieName, false)
	response := authRequest(t, &client, plain, "GET", "/api/v1/auth", "")
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("HTTP logout did not remove its session")
	}
	if view(secure).CurrentSessionID != secureSession {
		t.Fatal("HTTP logout cleared HTTPS session")
	}
	login(plain, auth.CookieName)
	plainSession = view(plain).CurrentSessionID
	logout(secure, auth.SecureCookieName, true)
	response = authRequest(t, &client, secure, "GET", "/api/v1/auth", "")
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("HTTPS logout fell back to the HTTP cookie")
	}
	if view(plain).CurrentSessionID != plainSession {
		t.Fatal("HTTPS logout cleared HTTP session")
	}
}
func TestHTTPSRecoveryReauthRequiresSecureCookieAndSetsRotatedHostCookie(t *testing.T) {
	service, _ := authServer(t)
	setup, err := service.BeginTOTP(context.Background(), auth.Identity{Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	codes, err := service.ConfirmTOTP(context.Background(), auth.Identity{Desktop: true}, auth.TOTPConfirm{SetupID: setup.SetupID, Code: code})
	if err != nil {
		t.Fatal(err)
	}
	api := New(Config{Auth: service})
	mux := http.NewServeMux()
	mux.Handle("/auth/", api.AuthHandler())
	mux.Handle("/api/v1/", api)
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := browser(t, server)
	response := authRequest(t, client, server, "POST", "/auth/login", `{"username":"owner","password":"correct-horse-battery"}`)
	var login auth.LoginResult
	_ = json.NewDecoder(response.Body).Decode(&login)
	response.Body.Close()
	if login.Authenticated || login.ChallengeID == "" {
		t.Fatal("expected second-factor login")
	}
	raw, _ := json.Marshal(auth.CodeInput{ChallengeID: login.ChallengeID, Code: codes.Codes[0]})
	response = authRequest(t, client, server, "POST", "/auth/login/recovery", string(raw))
	response.Body.Close()
	cookies := response.Cookies()
	if response.StatusCode != 200 || len(cookies) != 1 || cookies[0].Name != auth.SecureCookieName || !cookies[0].Secure {
		t.Fatalf("HTTPS factor login emitted wrong cookie: %d %+v", response.StatusCode, cookies)
	}
	oldToken := cookies[0].Value
	response = authRequest(t, client, server, "POST", "/api/v1/auth/reauth", `{"password":"correct-horse-battery"}`)
	var pending auth.LoginResult
	_ = json.NewDecoder(response.Body).Decode(&pending)
	response.Body.Close()
	if response.StatusCode != 200 || pending.Authenticated || pending.ChallengeID == "" || len(response.Cookies()) != 0 {
		t.Fatalf("incomplete HTTPS reauth changed cookie: %d %+v", response.StatusCode, pending)
	}
	raw, _ = json.Marshal(auth.CodeInput{ChallengeID: pending.ChallengeID, Code: codes.Codes[1]})
	request, _ := http.NewRequest("POST", server.URL+"/auth/login/recovery", strings.NewReader(string(raw)))
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: oldToken})
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("HTTPS reauth challenge accepted HTTP/old HTTPS cookie name")
	}
	response = authRequest(t, client, server, "POST", "/auth/login/recovery", string(raw))
	response.Body.Close()
	cookies = response.Cookies()
	if response.StatusCode != 200 || len(cookies) != 1 || cookies[0].Name != auth.SecureCookieName || !cookies[0].Secure || cookies[0].Value == oldToken {
		t.Fatalf("HTTPS completed reauth did not rotate __Host cookie: %d %+v", response.StatusCode, cookies)
	}
	response = authRequest(t, client, server, "GET", "/api/v1/auth", "")
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("rotated HTTPS cookie was not usable")
	}
}

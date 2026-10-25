package httpapi

import (
	"context"
	"crypto/tls"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/persistence"
)

func TestLoginCookieUsesVerifiedRequestScheme(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authentication, err := auth.New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = authentication.Setup(context.Background(), "owner", "strong-test-password"); err != nil {
		t.Fatal(err)
	}
	api := New(Config{Auth: authentication, PublicURL: "https://external.example"})
	for _, test := range []struct {
		scheme, origin, forwarded string
		secure                    bool
		status                    int
	}{{"http", "http://127.0.0.1:4545", "", false, 200}, {"https", "https://127.0.0.1:4545", "", true, 200}, {"http", "https://127.0.0.1:4545", "https", false, 403}, {"http", "", "https", false, 200}} {
		request := httptest.NewRequest("POST", test.scheme+"://127.0.0.1:4545/auth/login", strings.NewReader(`{"username":"owner","password":"strong-test-password"}`))
		request.RemoteAddr = "127.0.0.1:12345"
		if test.scheme == "https" {
			request.TLS = &tls.ConnectionState{}
		}
		request.Header.Set("Origin", test.origin)
		request.Header.Set("X-Forwarded-Proto", test.forwarded)
		response := httptest.NewRecorder()
		api.AuthHandler().ServeHTTP(response, request)
		cookies := response.Result().Cookies()
		if response.Code != test.status {
			t.Fatalf("%+v: status %d %s", test, response.Code, response.Body.String())
		}
		if test.status == 200 && (len(cookies) != 1 || cookies[0].Name != auth.SessionCookieName(test.secure) || cookies[0].Secure != test.secure || !cookies[0].HttpOnly) {
			t.Fatalf("%+v: incorrect cookie %+v", test, cookies)
		}
	}
}

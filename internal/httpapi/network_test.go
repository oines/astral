package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginCookieUsesCurrentBrowserScheme(t *testing.T) {
	// A configured HTTPS enrollment address must not break local HTTP sign-in.
	api := New(Config{OwnerToken: "owner-token", PublicURL: "https://external.example"})
	for _, test := range []struct {
		origin string
		secure bool
	}{{"http://127.0.0.1:4545", false}, {"https://127.0.0.1:4545", true}, {"", false}} {
		request := httptest.NewRequest("POST", "http://127.0.0.1:4545/auth/session", strings.NewReader(`{"token":"owner-token"}`))
		request.Header.Set("Origin", test.origin)
		response := httptest.NewRecorder()
		api.Login(response, request)
		cookies := response.Result().Cookies()
		if response.Code != 200 || len(cookies) != 1 || cookies[0].Secure != test.secure || !cookies[0].HttpOnly {
			t.Fatalf("origin %q returned incorrect cookie: %d %+v", test.origin, response.Code, cookies)
		}
	}
}

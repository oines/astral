package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestSessionCookiesAreSelectedOnlyByTrustedRequestScheme(t *testing.T) {
	f := newFixture(t)
	token := f.login(t)
	for _, secure := range []bool{false, true} {
		t.Run(SessionCookieName(secure), func(t *testing.T) {
			request := browserRequest("")
			if secure {
				request = webRequest()
			}
			correct, other := SessionCookieName(secure), SessionCookieName(!secure)
			// Even a valid token under the other transport's key must not authenticate.
			request.AddCookie(&http.Cookie{Name: other, Value: token})
			if _, err := f.s.Authenticate(request); errorCode(err) != "unauthorized" {
				t.Fatalf("other protocol cookie accepted: %v", err)
			}
			request.AddCookie(&http.Cookie{Name: correct, Value: "wrong"})
			if _, err := f.s.Authenticate(request); errorCode(err) != "unauthorized" {
				t.Fatalf("invalid selected cookie fell back to other key: %v", err)
			}
			request.Header.Del("Cookie")
			request.AddCookie(&http.Cookie{Name: correct, Value: token})
			if _, err := f.s.Authenticate(request); err != nil {
				t.Fatalf("correct protocol cookie rejected: %v", err)
			}
			response := httptest.NewRecorder()
			f.s.SetCookie(response, request, token)
			cookies := response.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != correct || cookies[0].Secure != secure || cookies[0].Path != "/" || cookies[0].Domain != "" || !cookies[0].HttpOnly {
				t.Fatalf("incorrect protocol cookie attributes: %+v", cookies)
			}
			deleted := httptest.NewRecorder()
			f.s.ClearCookie(deleted, request)
			cookies = deleted.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != correct || cookies[0].Secure != secure || cookies[0].Path != "/" || cookies[0].Domain != "" || cookies[0].MaxAge >= 0 {
				t.Fatalf("logout did not clear selected protocol cookie: %+v", cookies)
			}
		})
	}
}
func TestCookieSchemeDoesNotTrustOriginOrForwardedHeaders(t *testing.T) {
	f := newFixture(t)
	token := f.login(t)
	request := browserRequest("")
	request.Header.Set("Origin", "https://astral.example")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.AddCookie(&http.Cookie{Name: SecureCookieName, Value: token})
	if _, err := f.s.Authenticate(request); err == nil {
		t.Fatal("untrusted headers switched cookie selection to HTTPS")
	}
	response := httptest.NewRecorder()
	f.s.SetCookie(response, request, token)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != CookieName || cookies[0].Secure {
		t.Fatalf("untrusted headers selected secure cookie: %+v", cookies)
	}
	f.s.config.RequestOrigin = func(*http.Request) string { return "https://astral.example" }
	if _, err := f.s.Authenticate(request); err != nil {
		t.Fatalf("trusted proxy resolver did not select HTTPS cookie: %v", err)
	}
	response = httptest.NewRecorder()
	f.s.SetCookie(response, request, token)
	cookies = response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SecureCookieName || !cookies[0].Secure {
		t.Fatalf("trusted HTTPS resolver did not select __Host cookie: %+v", cookies)
	}
}
func TestHTTPSSecondFactorReauthUsesSecureCookieAndRotatesIt(t *testing.T) {
	f := newFixture(t)
	setup, _ := f.s.BeginTOTP(context.Background(), Identity{Desktop: true})
	code, _ := totp.GenerateCode(setup.Secret, f.now)
	codes, err := f.s.ConfirmTOTP(context.Background(), Identity{Desktop: true}, TOTPConfirm{SetupID: setup.SetupID, Code: code})
	if err != nil {
		t.Fatal(err)
	}
	login, _, _ := f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "")
	_, token, err := f.s.CompleteCode(context.Background(), f.webBrowserRequest(""), CodeInput{ChallengeID: login.ChallengeID, Code: codes.Codes[0]}, true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.s.Authenticate(f.webBrowserRequest(token))
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(6 * time.Minute)
	pending, early, err := f.s.Reauthenticate(context.Background(), id, "correct-horse-battery", "127.0.0.1")
	if err != nil || pending.Authenticated || early != "" {
		t.Fatalf("incomplete HTTPS reauth: %+v %v", pending, err)
	}
	wrong := webRequest()
	wrong.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	if _, _, err = f.s.CompleteCode(context.Background(), wrong, CodeInput{ChallengeID: pending.ChallengeID, Code: codes.Codes[1]}, true); errorCode(err) != "unauthorized" {
		t.Fatalf("HTTPS challenge accepted HTTP/legacy HTTPS cookie: %v", err)
	}
	code, _ = totp.GenerateCode(setup.Secret, f.now)
	result, rotated, err := f.s.CompleteCode(context.Background(), f.webBrowserRequest(token), CodeInput{ChallengeID: pending.ChallengeID, Code: code}, false)
	if err != nil || !result.Authenticated || rotated == "" || rotated == token {
		t.Fatalf("HTTPS TOTP reauth did not rotate: %+v %v", result, err)
	}
	if _, err = f.s.Authenticate(f.webBrowserRequest(token)); errorCode(err) != "unauthorized" {
		t.Fatalf("old HTTPS token accepted: %v", err)
	}
	current, err := f.s.Authenticate(f.webBrowserRequest(rotated))
	if err != nil || current.SessionID != id.SessionID {
		t.Fatalf("HTTPS rotated session mismatch: %+v %v", current, err)
	}
	response := httptest.NewRecorder()
	f.s.SetCookie(response, f.webBrowserRequest(rotated), rotated)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SecureCookieName || !cookies[0].Secure || !strings.HasPrefix(response.Header().Get("Set-Cookie"), "__Host-astral_session=") {
		t.Fatalf("HTTPS reauth emitted wrong cookie: %+v", cookies)
	}
}

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/pquerna/otp/totp"
)

type fixture struct {
	s     *Service
	store *persistence.Store
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Now().UTC()}
	var err error
	f.store, err = persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.store.Close() })
	f.s, err = New(f.store, &Config{DesktopToken: "private-desktop-token", Now: func() time.Time { return f.now }, WebOrigins: func() []string { return []string{"https://astral.example"} }})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Setup(context.Background(), "owner", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	return f
}
func browserRequest(token string) *http.Request {
	r := httptest.NewRequest("GET", "http://127.0.0.1:4545/api/v1/auth", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	if token != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	}
	return r
}
func (f *fixture) login(t *testing.T) string {
	t.Helper()
	result, token, err := f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "test browser")
	if err != nil || !result.Authenticated || token == "" {
		t.Fatalf("login: %+v %q %v", result, token, err)
	}
	return token
}
func errorCode(err error) string {
	if typed, ok := err.(*domain.Error); ok {
		return typed.Code
	}
	return ""
}
func TestDesktopHasSeparateLoopbackRuntimeIdentity(t *testing.T) {
	f := newFixture(t)
	for _, test := range []struct {
		remote, header string
		allowed        bool
	}{{"127.0.0.1:1", "Desktop private-desktop-token", true}, {"[::1]:1", "Desktop private-desktop-token", true}, {"192.168.1.2:1", "Desktop private-desktop-token", false}, {"127.0.0.1:1", "Bearer private-desktop-token", false}, {"127.0.0.1:1", "Desktop wrong", false}, {"127.0.0.1:1", "", false}} {
		r := browserRequest("")
		r.RemoteAddr = test.remote
		r.Header.Set("Authorization", test.header)
		id, err := f.s.Authenticate(r)
		if (err == nil) != test.allowed || err == nil && !id.Desktop {
			t.Fatalf("%+v: %+v %v", test, id, err)
		}
	}
	v, err := f.s.View(context.Background(), Identity{Desktop: true})
	if err != nil || len(v.Sessions) != 0 {
		t.Fatalf("desktop became a browser session: %+v %v", v, err)
	}
}
func TestBrowserSessionsPersistOnlyHashesAndRevokeRequests(t *testing.T) {
	f := newFixture(t)
	token := f.login(t)
	id, err := f.s.Authenticate(browserRequest(token))
	if err != nil {
		t.Fatal(err)
	}
	records, err := f.store.List(context.Background(), "auth_sessions")
	if err != nil || len(records) != 1 || strings.Contains(string(records[0].Data), token) || !strings.Contains(string(records[0].Data), digest(token)) {
		t.Fatalf("raw credential persisted: %+v %v", records, err)
	}
	ctx, release := f.s.Bind(context.Background(), id)
	defer release()
	if err = f.s.Revoke(context.Background(), Identity{Desktop: true}, id.SessionID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel in-flight requests")
	}
	if _, err = f.s.Authenticate(browserRequest(token)); errorCode(err) != "unauthorized" {
		t.Fatalf("revoked session accepted: %v", err)
	}
	token = f.login(t)
	restarted, err := New(f.store, &Config{Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Authenticate(browserRequest(token)); err != nil {
		t.Fatalf("session did not survive restart: %v", err)
	}
	f.now = f.now.Add(31 * 24 * time.Hour)
	if _, err = restarted.Authenticate(browserRequest(token)); errorCode(err) != "unauthorized" {
		t.Fatalf("server ignored expiry: %v", err)
	}
}
func TestAccountChangeAtomicallyRevokesEveryBrowserAndPreservesDesktop(t *testing.T) {
	f := newFixture(t)
	first, second := f.login(t), f.login(t)
	id, _ := f.s.Authenticate(browserRequest(first))
	ctx, release := f.s.Bind(context.Background(), id)
	defer release()
	if err := f.s.SetAccount(context.Background(), Identity{Desktop: true}, AccountInput{Username: "new-owner", Password: "new-secure-password"}); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first, second} {
		if _, err := f.s.Authenticate(browserRequest(token)); err == nil {
			t.Fatal("old session survived password change")
		}
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("active request survived password change")
	}
	r := browserRequest("")
	r.Header.Set("Authorization", "Desktop private-desktop-token")
	if _, err := f.s.Authenticate(r); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", ""); err == nil {
		t.Fatal("old password still works")
	}
}
func TestTOTPConfirmationReplayRecoveryAndChallengeExpiry(t *testing.T) {
	f := newFixture(t)
	setup, err := f.s.BeginTOTP(context.Background(), Identity{Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	status, _ := f.s.Status(context.Background())
	if len(status.Methods) != 0 {
		t.Fatal("unconfirmed TOTP became active")
	}
	code, _ := totp.GenerateCode(setup.Secret, f.now)
	recovery, err := f.s.ConfirmTOTP(context.Background(), Identity{Desktop: true}, TOTPConfirm{SetupID: setup.SetupID, Code: code})
	if err != nil || len(recovery.Codes) != 10 {
		t.Fatalf("confirm: %+v %v", recovery, err)
	}
	login, token, err := f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "")
	if err != nil || login.Authenticated || token != "" || login.ChallengeID == "" {
		t.Fatalf("password bypassed second factor: %+v %q %v", login, token, err)
	}
	r := browserRequest("")
	if _, _, err = f.s.CompleteCode(context.Background(), r, CodeInput{ChallengeID: login.ChallengeID, Code: code}, false); err == nil {
		t.Fatal("registration TOTP was replayable")
	}
	f.now = f.now.Add(30 * time.Second)
	code, _ = totp.GenerateCode(setup.Secret, f.now)
	result, token, err := f.s.CompleteCode(context.Background(), r, CodeInput{ChallengeID: login.ChallengeID, Code: code}, false)
	if err != nil || !result.Authenticated || token == "" {
		t.Fatalf("TOTP login: %+v %v", result, err)
	}
	if _, _, err = f.s.CompleteCode(context.Background(), r, CodeInput{ChallengeID: login.ChallengeID, Code: code}, false); err == nil {
		t.Fatal("successful challenge reused")
	}
	login, _, _ = f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "")
	result, _, err = f.s.CompleteCode(context.Background(), r, CodeInput{ChallengeID: login.ChallengeID, Code: recovery.Codes[0]}, true)
	if err != nil || !result.Authenticated {
		t.Fatalf("recovery: %+v %v", result, err)
	}
	login, _, _ = f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "")
	if _, _, err = f.s.CompleteCode(context.Background(), r, CodeInput{ChallengeID: login.ChallengeID, Code: recovery.Codes[0]}, true); err == nil {
		t.Fatal("recovery code reused")
	}
	f.now = f.now.Add(6 * time.Minute)
	if _, _, err = f.s.CompleteCode(context.Background(), r, CodeInput{ChallengeID: login.ChallengeID, Code: recovery.Codes[1]}, true); err == nil {
		t.Fatal("expired challenge accepted")
	}
}
func TestSensitiveMutationsRequireRecentAuthenticationAndReauthIsSessionBound(t *testing.T) {
	f := newFixture(t)
	token := f.login(t)
	id, _ := f.s.Authenticate(browserRequest(token))
	f.now = f.now.Add(6 * time.Minute)
	if _, err := f.s.BeginTOTP(context.Background(), id); errorCode(err) != "reauth_required" {
		t.Fatalf("stale session changed auth: %v", err)
	}
	result, rotatedToken, err := f.s.Reauthenticate(context.Background(), id, "correct-horse-battery", "127.0.0.1")
	if err != nil || !result.Authenticated {
		t.Fatalf("reauth %+v %v", result, err)
	}
	id, _ = f.s.Authenticate(browserRequest(rotatedToken))
	if _, err = f.s.BeginTOTP(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	setup, _ := f.s.BeginTOTP(context.Background(), Identity{Desktop: true})
	code, _ := totp.GenerateCode(setup.Secret, f.now)
	recovery, err := f.s.ConfirmTOTP(context.Background(), Identity{Desktop: true}, TOTPConfirm{SetupID: setup.SetupID, Code: code})
	if err != nil {
		t.Fatal(err)
	}
	login, _, _ := f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "")
	_, token, _ = f.s.CompleteCode(context.Background(), browserRequest(""), CodeInput{ChallengeID: login.ChallengeID, Code: recovery.Codes[0]}, true)
	id, _ = f.s.Authenticate(browserRequest(token))
	reauth, _, err := f.s.Reauthenticate(context.Background(), id, "correct-horse-battery", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.CompleteCode(context.Background(), browserRequest(""), CodeInput{ChallengeID: reauth.ChallengeID, Code: recovery.Codes[1]}, true); err == nil {
		t.Fatal("reauth not bound to browser cookie")
	}
	result, newToken, err := f.s.CompleteCode(context.Background(), browserRequest(token), CodeInput{ChallengeID: reauth.ChallengeID, Code: recovery.Codes[1]}, true)
	if err != nil || !result.Authenticated || newToken == "" || newToken == token {
		t.Fatalf("reauth issued another login: %+v %q %v", result, newToken, err)
	}
}
func TestResetClearsFactorsAndOldSecretHistory(t *testing.T) {
	f := newFixture(t)
	oldSecret := f.s.document.SecretID
	token := f.login(t)
	setup, _ := f.s.BeginTOTP(context.Background(), Identity{Desktop: true})
	code, _ := totp.GenerateCode(setup.Secret, f.now)
	_, err := f.s.ConfirmTOTP(context.Background(), Identity{Desktop: true}, TOTPConfirm{SetupID: setup.SetupID, Code: code})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Reset(context.Background(), "restored", "another-secure-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Secret(context.Background(), oldSecret); err == nil {
		t.Fatal("historical secret retained")
	}
	if _, err = f.s.Authenticate(browserRequest(token)); err == nil {
		t.Fatal("reset preserved session")
	}
	status, _ := f.s.Status(context.Background())
	if len(status.Methods) != 0 || status.Passwordless {
		t.Fatalf("reset factors %+v", status)
	}
	records, _ := f.store.Retained(context.Background(), "auth_account")
	for _, record := range records {
		var object map[string]any
		_ = json.Unmarshal(record.Data, &object)
		if strings.Contains(string(record.Data), "correct-horse") || strings.Contains(string(record.Data), setup.Secret) {
			t.Fatal("plaintext secrets in document history")
		}
	}
}
func TestRateLimitUsesPeerIPRatherThanPort(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 12; i++ {
		_, _, _ = f.s.Login(context.Background(), "owner", "bad", "192.0.2.1:1", "")
	}
	_, _, err := f.s.Login(context.Background(), "owner", "correct-horse-battery", "192.0.2.1:9999", "")
	if errorCode(err) != "rate_limited" {
		t.Fatalf("port bypassed rate limit: %v", err)
	}
}

func TestAuthenticationChangesRollbackWhenStorageFails(t *testing.T) {
	f := newFixture(t)
	token := f.login(t)
	id, _ := f.s.Authenticate(browserRequest(token))
	ctx, release := f.s.Bind(context.Background(), id)
	defer release()
	oldHash := f.s.state.PasswordHash
	oldSecret := f.s.document.SecretID
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	err := f.s.SetAccount(context.Background(), Identity{Desktop: true}, AccountInput{Username: "changed", Password: "another-safe-password"})
	if err == nil {
		t.Fatal("closed storage accepted account mutation")
	}
	if f.s.state.Username != "owner" || f.s.state.PasswordHash != oldHash || f.s.document.SecretID != oldSecret || f.s.sessions[id.SessionID] == nil {
		t.Fatal("failed transaction partially changed authentication")
	}
	select {
	case <-ctx.Done():
		t.Fatal("failed mutation revoked unchanged session")
	default:
	}
}
func TestRecoveryCodeConsumptionIsAtomicAcrossConcurrentChallenges(t *testing.T) {
	f := newFixture(t)
	setup, _ := f.s.BeginTOTP(context.Background(), Identity{Desktop: true})
	code, _ := totp.GenerateCode(setup.Secret, f.now)
	codes, err := f.s.ConfirmTOTP(context.Background(), Identity{Desktop: true}, TOTPConfirm{SetupID: setup.SetupID, Code: code})
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "")
	second, _, _ := f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "")
	results := make(chan bool, 2)
	for _, challengeID := range []string{first.ChallengeID, second.ChallengeID} {
		go func(id string) {
			result, _, err := f.s.CompleteCode(context.Background(), browserRequest(""), CodeInput{ChallengeID: id, Code: codes.Codes[0]}, true)
			results <- err == nil && result.Authenticated
		}(challengeID)
	}
	successes := 0
	for range 2 {
		if <-results {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("one recovery code issued %d sessions", successes)
	}
}

func TestFactorChangesKeepCurrentBrowserAndRevokeOtherSessions(t *testing.T) {
	f := newFixture(t)
	currentToken, otherToken := f.login(t), f.login(t)
	current, _ := f.s.Authenticate(browserRequest(currentToken))
	other, _ := f.s.Authenticate(browserRequest(otherToken))
	currentContext, releaseCurrent := f.s.Bind(context.Background(), current)
	defer releaseCurrent()
	otherContext, releaseOther := f.s.Bind(context.Background(), other)
	defer releaseOther()
	setup, err := f.s.BeginTOTP(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(setup.Secret, f.now)
	_, err = f.s.ConfirmTOTP(context.Background(), current, TOTPConfirm{SetupID: setup.SetupID, Code: code})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Authenticate(browserRequest(currentToken)); err != nil {
		t.Fatalf("factor update logged out current browser: %v", err)
	}
	select {
	case <-currentContext.Done():
		t.Fatal("factor update canceled current browser requests")
	default:
	}
	if _, err = f.s.Authenticate(browserRequest(otherToken)); err == nil {
		t.Fatal("factor update left another browser signed in")
	}
	select {
	case <-otherContext.Done():
	default:
		t.Fatal("factor update left another browser's active connection alive")
	}
	pending, _, err := f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "")
	if err != nil || pending.ChallengeID == "" {
		t.Fatal("expected a pending second-factor challenge")
	}
	codes, err := f.s.RegenerateRecoveryCodes(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.CompleteCode(context.Background(), browserRequest(""), CodeInput{ChallengeID: pending.ChallengeID, Code: codes.Codes[0]}, true); err == nil {
		t.Fatal("security change kept an old pending login challenge alive")
	}
	if err = f.s.SetAccount(context.Background(), current, AccountInput{Username: "owner", Password: "replacement-password"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Authenticate(browserRequest(currentToken)); err == nil {
		t.Fatal("password change kept current browser signed in")
	}
	select {
	case <-currentContext.Done():
	default:
		t.Fatal("password change kept current browser connection alive")
	}
}

func TestPasswordOnlyReauthRotatesTokenWithoutChangingSessionOrActiveRequests(t *testing.T) {
	f := newFixture(t)
	oldToken := f.login(t)
	oldIdentity, _ := f.s.Authenticate(browserRequest(oldToken))
	active, release := f.s.Bind(context.Background(), oldIdentity)
	defer release()
	f.now = f.now.Add(6 * time.Minute)
	result, newToken, err := f.s.Reauthenticate(context.Background(), oldIdentity, "correct-horse-battery", "127.0.0.1")
	if err != nil || !result.Authenticated || newToken == "" || newToken == oldToken {
		t.Fatalf("reauth did not rotate token: %+v %v", result, err)
	}
	if _, err = f.s.Authenticate(browserRequest(oldToken)); errorCode(err) != "unauthorized" {
		t.Fatalf("old cookie survived rotation: %v", err)
	}
	current, err := f.s.Authenticate(browserRequest(newToken))
	if err != nil || current.SessionID != oldIdentity.SessionID {
		t.Fatalf("rotation created a different session: %+v %v", current, err)
	}
	select {
	case <-active.Done():
		t.Fatal("token rotation revoked an already authenticated request")
	default:
	}
	if err = f.s.requireRecentLocked(current); err != nil {
		t.Fatalf("new token did not gain recent authentication: %v", err)
	}
	if err = f.s.requireRecentLocked(oldIdentity); errorCode(err) != "unauthorized" {
		t.Fatalf("queued old-token request inherited fresh privileges: %v", err)
	}
}
func TestSecondFactorReauthRotatesOnlyAfterFullAuthentication(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "totp", true: "recovery"}[recovery], func(t *testing.T) {
			f := newFixture(t)
			setup, _ := f.s.BeginTOTP(context.Background(), Identity{Desktop: true})
			code, _ := totp.GenerateCode(setup.Secret, f.now)
			codes, err := f.s.ConfirmTOTP(context.Background(), Identity{Desktop: true}, TOTPConfirm{SetupID: setup.SetupID, Code: code})
			if err != nil {
				t.Fatal(err)
			}
			login, _, _ := f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "")
			_, oldToken, err := f.s.CompleteCode(context.Background(), browserRequest(""), CodeInput{ChallengeID: login.ChallengeID, Code: codes.Codes[0]}, true)
			if err != nil {
				t.Fatal(err)
			}
			identity, _ := f.s.Authenticate(browserRequest(oldToken))
			active, release := f.s.Bind(context.Background(), identity)
			defer release()
			f.now = f.now.Add(6 * time.Minute)
			pending, earlyToken, err := f.s.Reauthenticate(context.Background(), identity, "correct-horse-battery", "127.0.0.1")
			if err != nil || pending.Authenticated || pending.ChallengeID == "" || earlyToken != "" {
				t.Fatalf("password alone completed second-factor reauth: %+v %v", pending, err)
			}
			if err = f.s.requireRecentLocked(identity); errorCode(err) != "reauth_required" {
				t.Fatalf("incomplete second factor elevated session: %v", err)
			}
			code, _ = totp.GenerateCode(setup.Secret, f.now)
			if recovery {
				code = codes.Codes[1]
			}
			result, newToken, err := f.s.CompleteCode(context.Background(), browserRequest(oldToken), CodeInput{ChallengeID: pending.ChallengeID, Code: code}, recovery)
			if err != nil || !result.Authenticated || newToken == "" || newToken == oldToken {
				t.Fatalf("second factor did not rotate: %+v %v", result, err)
			}
			if _, err = f.s.Authenticate(browserRequest(oldToken)); errorCode(err) != "unauthorized" {
				t.Fatalf("old token survived: %v", err)
			}
			current, err := f.s.Authenticate(browserRequest(newToken))
			if err != nil || current.SessionID != identity.SessionID {
				t.Fatalf("same session not retained: %+v %v", current, err)
			}
			if err = f.s.requireRecentLocked(current); err != nil {
				t.Fatal(err)
			}
			select {
			case <-active.Done():
				t.Fatal("token rotation canceled existing request")
			default:
			}
		})
	}
}
func TestFailedReauthStorageDoesNotReplaceValidToken(t *testing.T) {
	f := newFixture(t)
	token := f.login(t)
	id, _ := f.s.Authenticate(browserRequest(token))
	previousHash := f.s.sessions[id.SessionID].TokenHash
	previousTime := f.s.sessions[id.SessionID].AuthenticatedAt
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	result, newToken, err := f.s.Reauthenticate(context.Background(), id, "correct-horse-battery", "127.0.0.1")
	if err == nil || result.Authenticated || newToken != "" {
		t.Fatalf("storage failure published rotated token: %+v %q %v", result, newToken, err)
	}
	session := f.s.sessions[id.SessionID]
	if session.TokenHash != previousHash || !session.AuthenticatedAt.Equal(previousTime) {
		t.Fatal("failed transaction changed the in-memory credential")
	}
	if _, err = f.s.Authenticate(browserRequest(token)); err != nil {
		t.Fatalf("failed rotation replaced the valid old token: %v", err)
	}
}

func TestReauthCookieRetainsOriginalAbsoluteSessionExpiry(t *testing.T) {
	f := newFixture(t)
	token := f.login(t)
	id, _ := f.s.Authenticate(browserRequest(token))
	originalExpiry := f.s.sessions[id.SessionID].ExpiresAt
	f.now = f.now.Add(time.Hour)
	_, rotated, err := f.s.Reauthenticate(context.Background(), id, "correct-horse-battery", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	f.s.SetCookie(response, browserRequest(rotated), rotated)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing rotated cookie")
	}
	if cookies[0].MaxAge != int(originalExpiry.Sub(f.now).Seconds()) || !cookies[0].Expires.Equal(originalExpiry.Truncate(time.Second)) {
		t.Fatalf("rotation extended original expiry: %+v", cookies[0])
	}
}

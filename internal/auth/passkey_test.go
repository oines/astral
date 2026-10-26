package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// This small virtual authenticator produces real P-256 registration material
// and signed assertions. Tests exercise the library verification boundary rather
// than mocking successful Passkey validation.
type virtualAuthenticator struct {
	key    *ecdsa.PrivateKey
	id     []byte
	count  uint32
	handle []byte
}

func newAuthenticator(t *testing.T, handle []byte) *virtualAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	if _, err = rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return &virtualAuthenticator{key: key, id: id, handle: handle}
}
func webRequest() *http.Request {
	r := httptest.NewRequest("POST", "https://astral.example/auth/login/passkey/begin", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Origin", "https://astral.example")
	return r
}
func (f *fixture) webBrowserRequest(token string) *http.Request {
	r := webRequest()
	if token != "" {
		r.AddCookie(&http.Cookie{Name: SessionCookieName(strings.HasPrefix(f.s.Origin(r), "https://")), Value: token})
	}
	return r
}
func b64(v []byte) string { return base64.RawURLEncoding.EncodeToString(v) }
func ceremonyChallenge(t *testing.T, c Ceremony) string {
	t.Helper()
	var options struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(c.PublicKey, &options); err != nil {
		t.Fatal(err)
	}
	return options.Challenge
}
func (v *virtualAuthenticator) registration(t *testing.T, c Ceremony, origin string) json.RawMessage {
	t.Helper()
	client, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": ceremonyChallenge(t, c), "origin": origin})
	rp := sha256.Sum256([]byte("astral.example"))
	data := append([]byte{}, rp[:]...)
	data = append(data, 0x45, 0, 0, 0, 0)
	data = append(data, make([]byte, 16)...)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(v.id)))
	data = append(data, length...)
	data = append(data, v.id...)
	key, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: v.key.X.FillBytes(make([]byte, 32)), -3: v.key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, key...)
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": data})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": b64(v.id), "rawId": b64(v.id), "type": "public-key", "authenticatorAttachment": "platform", "response": map[string]any{"clientDataJSON": b64(client), "attestationObject": b64(attestation), "transports": []string{"internal"}}, "clientExtensionResults": map[string]any{}})
	return raw
}
func (v *virtualAuthenticator) assertion(t *testing.T, c Ceremony, origin string, uv bool) json.RawMessage {
	t.Helper()
	client, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": ceremonyChallenge(t, c), "origin": origin})
	rp := sha256.Sum256([]byte("astral.example"))
	data := append([]byte{}, rp[:]...)
	flags := byte(0x01)
	if uv {
		flags |= 0x04
	}
	data = append(data, flags)
	v.count++
	counter := make([]byte, 4)
	binary.BigEndian.PutUint32(counter, v.count)
	data = append(data, counter...)
	clientHash := sha256.Sum256(client)
	signed := append(append([]byte{}, data...), clientHash[:]...)
	hash := sha256.Sum256(signed)
	signature, err := ecdsa.SignASN1(rand.Reader, v.key, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": b64(v.id), "rawId": b64(v.id), "type": "public-key", "authenticatorAttachment": "platform", "response": map[string]any{"clientDataJSON": b64(client), "authenticatorData": b64(data), "signature": b64(signature), "userHandle": b64(v.handle)}, "clientExtensionResults": map[string]any{}})
	return raw
}
func registerAuthenticator(t *testing.T, f *fixture) *virtualAuthenticator {
	t.Helper()
	v := newAuthenticator(t, f.s.state.UserID)
	c, err := f.s.BeginPasskeyRegistration(context.Background(), webRequest(), Identity{Desktop: true}, "Test Passkey")
	if err != nil {
		t.Fatal(err)
	}
	var options map[string]any
	_ = json.Unmarshal(c.PublicKey, &options)
	selection := options["authenticatorSelection"].(map[string]any)
	if selection["residentKey"] != "required" || selection["userVerification"] != "required" {
		t.Fatalf("weak registration policy: %+v", selection)
	}
	key, err := f.s.FinishPasskeyRegistration(context.Background(), webRequest(), Identity{Desktop: true}, PasskeyFinishInput{ChallengeID: c.ChallengeID, Credential: v.registration(t, c, "https://astral.example")})
	if err != nil || key.RPID != "astral.example" {
		t.Fatalf("registration: %+v %v", key, err)
	}
	return v
}
func TestPasskeyCanReplaceTOTPButPasswordlessMustBeExplicit(t *testing.T) {
	f := newFixture(t)
	v := registerAuthenticator(t, f)
	if _, err := f.s.BeginPasskeyLogin(context.Background(), webRequest(), ""); errorCode(err) != "passwordless_disabled" {
		t.Fatalf("implicit passwordless enabled: %v", err)
	}
	login, token, err := f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "")
	if err != nil || token != "" || login.Authenticated || login.ChallengeID == "" {
		t.Fatalf("Passkey factor ignored: %+v %v", login, err)
	}
	c, err := f.s.BeginPasskeyLogin(context.Background(), webRequest(), login.ChallengeID)
	if err != nil {
		t.Fatal(err)
	}
	result, token, err := f.s.FinishPasskeyLogin(context.Background(), webRequest(), PasskeyFinishInput{ChallengeID: c.ChallengeID, Credential: v.assertion(t, c, "https://astral.example", true)})
	if err != nil || !result.Authenticated || token == "" {
		t.Fatalf("password+Passkey: %+v %v", result, err)
	}
	if _, err = f.s.BeginPasskeyLogin(context.Background(), webRequest(), login.ChallengeID); err == nil {
		t.Fatal("parent password challenge replayed")
	}
	if err = f.s.SetPasswordless(context.Background(), Identity{Desktop: true}, true); err != nil {
		t.Fatal(err)
	}
	c, err = f.s.BeginPasskeyLogin(context.Background(), webRequest(), "")
	if err != nil {
		t.Fatal(err)
	}
	var options struct {
		Allow []any  `json:"allowCredentials"`
		UV    string `json:"userVerification"`
	}
	_ = json.Unmarshal(c.PublicKey, &options)
	if len(options.Allow) != 0 || options.UV != "required" {
		t.Fatalf("not discoverable UV login: %s", c.PublicKey)
	}
	result, token, err = f.s.FinishPasskeyLogin(context.Background(), webRequest(), PasskeyFinishInput{ChallengeID: c.ChallengeID, Credential: v.assertion(t, c, "https://astral.example", true)})
	if err != nil || !result.Authenticated || token == "" {
		t.Fatalf("passwordless: %+v %v", result, err)
	}
}
func TestPasskeyRejectsMissingUVWrongOriginReplayAndHTTP(t *testing.T) {
	f := newFixture(t)
	v := registerAuthenticator(t, f)
	_ = f.s.SetPasswordless(context.Background(), Identity{Desktop: true}, true)
	for _, test := range []struct {
		origin string
		uv     bool
	}{{"https://astral.example", false}, {"https://evil.example", true}} {
		c, err := f.s.BeginPasskeyLogin(context.Background(), webRequest(), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = f.s.FinishPasskeyLogin(context.Background(), webRequest(), PasskeyFinishInput{ChallengeID: c.ChallengeID, Credential: v.assertion(t, c, test.origin, test.uv)}); err == nil {
			t.Fatalf("invalid assertion accepted: %+v", test)
		}
	}
	c, _ := f.s.BeginPasskeyLogin(context.Background(), webRequest(), "")
	input := PasskeyFinishInput{ChallengeID: c.ChallengeID, Credential: v.assertion(t, c, "https://astral.example", true)}
	if _, _, err := f.s.FinishPasskeyLogin(context.Background(), webRequest(), input); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.FinishPasskeyLogin(context.Background(), webRequest(), input); err == nil {
		t.Fatal("assertion replay accepted")
	}
	r := httptest.NewRequest("POST", "http://astral.example/auth/login/passkey/begin", nil)
	r.Header.Set("Origin", "https://astral.example")
	r.Header.Set("X-Forwarded-Proto", "https")
	if _, err := f.s.BeginPasskeyLogin(context.Background(), r, ""); errorCode(err) != "passkey_unavailable" {
		t.Fatalf("HTTP/spoofed HTTPS accepted: %v", err)
	}
}
func TestPasskeyOnlyReauthCannotBeCompletedByRecoveryCode(t *testing.T) {
	f := newFixture(t)
	v := registerAuthenticator(t, f)
	codes, err := f.s.RegenerateRecoveryCodes(context.Background(), Identity{Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = f.s.SetPasswordless(context.Background(), Identity{Desktop: true}, true)
	c, _ := f.s.BeginPasskeyLogin(context.Background(), webRequest(), "")
	_, token, err := f.s.FinishPasskeyLogin(context.Background(), webRequest(), PasskeyFinishInput{ChallengeID: c.ChallengeID, Credential: v.assertion(t, c, "https://astral.example", true)})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := f.s.Authenticate(f.webBrowserRequest(token))
	f.now = f.now.Add(6 * time.Minute)
	login, err := f.s.StartPasskeyReauthentication(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.requireRecentLocked(id); errorCode(err) != "reauth_required" {
		t.Fatalf("incomplete Passkey reauth elevated session: %v", err)
	}
	if _, _, err = f.s.CompleteCode(context.Background(), f.webBrowserRequest(token), CodeInput{ChallengeID: login.ChallengeID, Code: codes.Codes[0]}, true); err == nil {
		t.Fatal("Passkey-only reauth downgraded to a recovery code")
	}
	r := f.webBrowserRequest(token)
	c, err = f.s.BeginPasskeyLogin(context.Background(), r, login.ChallengeID)
	if err != nil {
		t.Fatal(err)
	}
	result, newToken, err := f.s.FinishPasskeyLogin(context.Background(), r, PasskeyFinishInput{ChallengeID: c.ChallengeID, Credential: v.assertion(t, c, "https://astral.example", true)})
	if err != nil || !result.Authenticated || newToken == "" || newToken == token {
		t.Fatalf("Passkey reauth: %+v %q %v", result, newToken, err)
	}
	if _, err = f.s.Authenticate(f.webBrowserRequest(token)); errorCode(err) != "unauthorized" {
		t.Fatalf("old Passkey reauth token survived rotation: %v", err)
	}
	current, err := f.s.Authenticate(f.webBrowserRequest(newToken))
	if err != nil || current.SessionID != id.SessionID {
		t.Fatalf("Passkey reauth changed session: %+v %v", current, err)
	}
	if err = f.s.requireRecentLocked(current); err != nil {
		t.Fatal(err)
	}
}
func TestCancelledPasskeyKeepsPasswordChallengeAvailableForRecovery(t *testing.T) {
	f := newFixture(t)
	v := registerAuthenticator(t, f)
	codes, err := f.s.RegenerateRecoveryCodes(context.Background(), Identity{Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	login, _, _ := f.s.Login(context.Background(), "owner", "correct-horse-battery", "127.0.0.1", "")
	c, err := f.s.BeginPasskeyLogin(context.Background(), webRequest(), login.ChallengeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.CompleteCode(context.Background(), f.webBrowserRequest(""), CodeInput{ChallengeID: login.ChallengeID, Code: codes.Codes[0]}, true); err != nil {
		t.Fatalf("Passkey cancellation blocked alternative: %v", err)
	}
	if _, _, err = f.s.FinishPasskeyLogin(context.Background(), webRequest(), PasskeyFinishInput{ChallengeID: c.ChallengeID, Credential: v.assertion(t, c, "https://astral.example", true)}); err == nil {
		t.Fatal("same password authorization issued multiple sessions")
	}
}

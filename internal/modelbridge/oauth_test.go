package modelbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral-core/models/auth"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }
func authResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func saveAuthProvider(t *testing.T, b *Bridge, id string) domain.Provider {
	t.Helper()
	p := FindPreset(id)
	provider := domain.Provider{PresetID: p.ID, Name: p.Name, BaseURL: p.BaseURL, Protocol: p.Protocol, Dialect: p.Dialect}
	if err := b.SaveProvider(context.Background(), &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestCopilotAuthorizationThrottlesPollingAndBoundsCredentials(t *testing.T) {
	b := testBridge(t)
	ctx := context.Background()
	provider := saveAuthProvider(t, b, "github-copilot")
	polls, upstream := 0, 0
	b.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://github.com/login/device/code":
			return authResponse(200, `{"device_code":"private-code","user_code":"USER-CODE","verification_uri":"https://github.com/login/device","interval":5,"expires_in":600}`), nil
		case "https://github.com/login/oauth/access_token":
			polls++
			if polls == 1 {
				return authResponse(200, `{"error":"authorization_pending"}`), nil
			}
			if polls == 2 {
				return authResponse(200, `{"error":"slow_down"}`), nil
			}
			return authResponse(200, `{"access_token":"fixture-oauth-token"}`), nil
		case "https://api.githubcopilot.com/models":
			upstream++
			if req.Header.Get("Authorization") != "Bearer fixture-oauth-token" || req.Header.Get("X-Api-Key") != "" || req.Header.Get("X-GitHub-Api-Version") != "2026-07-01" {
				t.Fatal("incorrect model authorization headers")
			}
			return authResponse(200, `{"data":[]}`), nil
		default:
			t.Fatalf("unexpected destination %s", req.URL)
			return nil, nil
		}
	})}
	flow, err := b.StartAuthorization(ctx, provider.ID)
	if err != nil || flow.Status != "pending" {
		t.Fatalf("start: %+v %v", flow, err)
	}
	public, _ := json.Marshal(flow)
	if strings.Contains(string(public), "private-code") {
		t.Fatal("private device code exposed")
	}
	if _, err := b.PollAuthorization(ctx, flow.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PollAuthorization(ctx, flow.ID); err != nil || polls != 1 {
		t.Fatalf("poll interval ignored: %d %v", polls, err)
	}
	b.authFlows[flow.ID].state.LastPoll = time.Time{}
	slow, err := b.PollAuthorization(ctx, flow.ID)
	if err != nil || slow.Interval != 10 {
		t.Fatalf("slow_down: %+v %v", slow, err)
	}
	b.authFlows[flow.ID].state.LastPoll = time.Time{}
	connected, err := b.PollAuthorization(ctx, flow.ID)
	if err != nil || connected.Status != "connected" || connected.Provider == nil || connected.Provider.CredentialID == "" {
		t.Fatalf("connect: %+v %v", connected, err)
	}
	if b.authFlows[flow.ID].state.DeviceCode != "" {
		t.Fatal("device code retained after success")
	}
	public, _ = json.Marshal(connected)
	if strings.Contains(string(public), "fixture-oauth-token") {
		t.Fatal("access token exposed")
	}
	if _, err = b.Catalog(ctx, provider.ID); err != nil || upstream != 1 {
		t.Fatalf("authorized catalog: %v", err)
	}
	for _, address := range []string{"https://unrelated.example/models", "http://api.githubcopilot.com/models"} {
		req, _ := http.NewRequest(http.MethodGet, address, nil)
		if _, err := b.oauthClient(*connected.Provider).Do(req); err == nil {
			t.Fatal("OAuth credentials allowed outside the authorized host")
		}
	}
	if upstream != 1 {
		t.Fatal("rejected request reached transport")
	}
}

func TestCodexDeviceExchangeAndRefreshPreserveConfigurationRevision(t *testing.T) {
	b := testBridge(t)
	ctx := context.Background()
	provider := saveAuthProvider(t, b, "codex")
	idToken := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account"}}`)) + ".signature"
	exchanges, refreshes := 0, 0
	b.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			return authResponse(200, `{"device_auth_id":"device-id","usercode":"CODEX-CODE","interval":"5"}`), nil
		case "/api/accounts/deviceauth/token":
			return authResponse(200, `{"authorization_code":"auth-code","code_verifier":"verifier"}`), nil
		case "/oauth/token":
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if req.Form.Get("grant_type") == "authorization_code" {
				exchanges++
				if req.Form.Get("code_verifier") != "verifier" || req.Form.Get("code") != "auth-code" {
					t.Fatal("missing PKCE exchange")
				}
				return authResponse(200, string(raw(map[string]any{"access_token": "first-token", "refresh_token": "refresh-token", "id_token": idToken, "expires_in": 60}))), nil
			}
			refreshes++
			if req.Form.Get("refresh_token") != "refresh-token" {
				t.Fatal("lost refresh token")
			}
			return authResponse(200, `{"access_token":"next-token","expires_in":3600}`), nil
		case "/backend-api/codex/models":
			if req.Header.Get("Authorization") != "Bearer next-token" || req.Header.Get("ChatGPT-Account-Id") != "fixture-account" {
				t.Fatal("refreshed account headers missing")
			}
			return authResponse(200, `{"models":[]}`), nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})}
	flow, err := b.StartAuthorization(ctx, provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	connected, err := b.PollAuthorization(ctx, flow.ID)
	if err != nil || connected.Status != "connected" {
		t.Fatalf("exchange: %+v %v", connected, err)
	}
	before := *connected.Provider
	if _, err := b.Catalog(ctx, before.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Catalog(ctx, before.ID); err != nil {
		t.Fatal(err)
	}
	var after domain.Provider
	if err := b.Store.Get(ctx, "providers", before.ID, &after); err != nil {
		t.Fatal(err)
	}
	if exchanges != 1 || refreshes != 1 || before.Revision != after.Revision || before.CredentialID != after.CredentialID {
		t.Fatal("token refresh changed the model configuration or repeated unnecessarily")
	}
}

func TestAuthorizationRejectsRedirectsAndExpiresWithoutPolling(t *testing.T) {
	b := testBridge(t)
	ctx := context.Background()
	provider := saveAuthProvider(t, b, "github-copilot")
	calls := 0
	b.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		response := authResponse(307, `{}`)
		response.Header.Set("Location", "https://unexpected.example/collect")
		return response, nil
	})}
	if _, err := b.StartAuthorization(ctx, provider.ID); err == nil || calls != 1 {
		t.Fatal("authorization followed a redirect")
	}
	b.authFlows = map[string]*authFlow{"expired": {public: Authorization{ID: "expired", Status: "pending"}, state: auth.Flow{Status: "pending", PresetID: "github-copilot", ExpiresAt: time.Now().Add(-time.Second)}}}
	flow, err := b.PollAuthorization(ctx, "expired")
	if err != nil || flow.Status != "expired" || calls != 1 {
		t.Fatal("expired flow contacted upstream")
	}
}

func TestOAuthReauthorizationPreservesFrozenGrant(t *testing.T) {
	b := testBridge(t)
	ctx := context.Background()
	p := saveAuthProvider(t, b, "github-copilot")
	grants := 0
	b.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "github.com":
			if r.URL.Path == "/login/device/code" {
				return authResponse(200, `{"device_code":"device","user_code":"USER","verification_uri":"https://github.com/login/device"}`), nil
			}
			grants++
			return authResponse(200, `{"access_token":"grant-`+strconv.Itoa(grants)+`"}`), nil
		case "api.githubcopilot.com":
			if r.Header.Get("Authorization") != "Bearer grant-1" {
				t.Fatal("frozen grant silently changed accounts")
			}
			return authResponse(200, `{}`), nil
		}
		t.Fatalf("unexpected OAuth destination %s", r.URL)
		return nil, nil
	})}
	connect := func() domain.Provider {
		flow, err := b.StartAuthorization(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		done, err := b.PollAuthorization(ctx, flow.ID)
		if err != nil {
			t.Fatal(err)
		}
		return *done.Provider
	}
	old := connect()
	next := connect()
	if old.CredentialID == next.CredentialID || old.ReplayRevision == next.ReplayRevision {
		t.Fatal("new authorization reused frozen credential or replay scope")
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/responses", nil)
	resp, err := b.oauthClient(old).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestCodexRefreshRejectsAccountSwitchAndKeepsTransientFailureRetryable(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			b := testBridge(t)
			ctx := context.Background()
			p := saveAuthProvider(t, b, "codex")
			original := string(raw(oauthTokens{AccessToken: "original", RefreshToken: "refresh", AccountID: "first"}))
			if err := b.Store.Update(ctx, func(tx *persistence.Tx) error {
				id, err := tx.PutSecret(ctx, original)
				p.CredentialID = id
				return err
			}); err != nil {
				t.Fatal(err)
			}
			idToken := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"chatgpt_account_id":"second"}`)) + ".signature"
			b.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return authResponse(status, string(raw(map[string]any{"access_token": "changed", "id_token": idToken, "expires_in": 3600}))), nil
			})}
			_, err := b.oauthCredentials(ctx, p)
			if err == nil {
				t.Fatal("invalid refresh accepted")
			}
			if status == 503 && strings.Contains(err.Error(), "sign in") {
				t.Fatal("temporary refresh failure discarded valid grant")
			}
			after, err := b.Store.Secret(ctx, p.CredentialID)
			if err != nil || after != original {
				t.Fatal("failed refresh altered frozen credential")
			}
		})
	}
}

func TestOAuthInferenceDoesNotFollowRedirectOrRetryPermissionFailure(t *testing.T) {
	for _, status := range []int{307, 401, 403} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			b := testBridge(t)
			ctx := context.Background()
			p := saveAuthProvider(t, b, "github-copilot")
			if err := b.Store.Update(ctx, func(tx *persistence.Tx) error {
				id, err := tx.PutSecret(ctx, `{"access_token":"fixture"}`)
				p.CredentialID = id
				return err
			}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			b.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("ChatGPT-Account-Id") != "" || r.Header.Get("Proxy-Authorization") != "" {
					t.Fatal("foreign credentials reached Copilot")
				}
				resp := authResponse(status, `{}`)
				resp.Header.Set("Location", "https://api.githubcopilot.com/other")
				return resp, nil
			})}
			req, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/responses", strings.NewReader(`{}`))
			req.Header.Set("ChatGPT-Account-Id", "unrelated")
			req.Header.Set("Proxy-Authorization", "secret")
			resp, err := b.oauthClient(p).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if calls != 1 || resp.StatusCode != status {
				t.Fatalf("OAuth transport changed request intent calls=%d status=%d", calls, resp.StatusCode)
			}
		})
	}
}

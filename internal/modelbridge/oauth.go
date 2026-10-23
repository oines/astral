package modelbridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

// Public native-app client IDs use device authorization. No existing CLI or
// browser credentials are imported, and the user approves each new grant.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// Copilot CLI OAuth tokens are accepted directly by api.githubcopilot.com.
// Do not mix this client with the VS Code client's internal token exchange.
const copilotClientID = "Ov23li8tweQw6odWQebz"

type Authorization struct {
	ID              string           `json:"id"`
	Status          string           `json:"status"`
	VerificationURL string           `json:"verification_url,omitempty"`
	UserCode        string           `json:"user_code,omitempty"`
	Interval        int              `json:"interval"`
	ExpiresAt       time.Time        `json:"expires_at"`
	Provider        *domain.Provider `json:"provider,omitempty"`
}
type authFlow struct {
	public     Authorization
	providerID string
	presetID   string
	deviceCode string
	lastPoll   time.Time
}
type oauthTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	ExpiresIn    int       `json:"expires_in,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
}

func (b *Bridge) authRequest(ctx context.Context, endpoint string, payload any, form bool) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var body io.Reader
	contentType := "application/json"
	if form {
		body = strings.NewReader(payload.(url.Values).Encode())
		contentType = "application/x-www-form-urlencoded"
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Astral/0.1.0")
	client := b.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	client = &copy
	response, err := client.Do(req)
	if err != nil {
		return nil, 0, domain.Offline("authorization service is unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return data, response.StatusCode, err
}

func (b *Bridge) StartAuthorization(ctx context.Context, providerID string) (Authorization, error) {
	var p domain.Provider
	if err := b.Store.Get(ctx, "providers", providerID, &p); err != nil {
		return Authorization{}, err
	}
	preset := FindPreset(p.PresetID)
	if preset == nil || preset.Auth != "oauth" {
		return Authorization{}, domain.Invalid("this provider uses an API key")
	}
	endpoint := "https://github.com/login/device/code"
	payload := map[string]string{"client_id": copilotClientID, "scope": "read:user"}
	if p.PresetID == "codex" {
		endpoint = "https://auth.openai.com/api/accounts/deviceauth/usercode"
		payload = map[string]string{"client_id": codexClientID}
	}
	data, status, err := b.authRequest(ctx, endpoint, payload, false)
	if err != nil {
		return Authorization{}, err
	}
	if status < 200 || status >= 300 {
		return Authorization{}, domain.Invalid(fmt.Sprintf("authorization could not start (HTTP %d)", status))
	}
	var reply struct {
		DeviceCode      string          `json:"device_code"`
		DeviceAuthID    string          `json:"device_auth_id"`
		UserCode        string          `json:"user_code"`
		Usercode        string          `json:"usercode"`
		VerificationURL string          `json:"verification_uri"`
		Interval        json.RawMessage `json:"interval"`
		ExpiresIn       int             `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return Authorization{}, domain.Invalid("invalid authorization response")
	}
	if reply.UserCode == "" {
		reply.UserCode = reply.Usercode
	}
	if reply.DeviceCode == "" {
		reply.DeviceCode = reply.DeviceAuthID
	}
	interval, _ := strconv.Atoi(strings.Trim(string(reply.Interval), `"`))
	interval = max(5, interval)
	if reply.ExpiresIn < 1 {
		reply.ExpiresIn = 900
	}
	if p.PresetID == "codex" {
		reply.VerificationURL = "https://auth.openai.com/codex/device"
	}
	allowed := "https://github.com/login/device"
	if p.PresetID == "codex" {
		allowed = "https://auth.openai.com/codex/device"
	}
	if reply.DeviceCode == "" || reply.UserCode == "" || reply.VerificationURL != allowed {
		return Authorization{}, domain.Invalid("authorization returned an unexpected sign-in address")
	}
	public := Authorization{ID: domain.ID("authorization"), Status: "pending", VerificationURL: reply.VerificationURL, UserCode: reply.UserCode, Interval: interval, ExpiresAt: time.Now().Add(time.Duration(min(reply.ExpiresIn, 900)) * time.Second)}
	b.authMu.Lock()
	defer b.authMu.Unlock()
	if b.authFlows == nil {
		b.authFlows = map[string]*authFlow{}
	}
	for id, flow := range b.authFlows {
		if time.Now().After(flow.public.ExpiresAt) || flow.providerID == providerID {
			delete(b.authFlows, id)
		}
	}
	b.authFlows[public.ID] = &authFlow{public: public, providerID: providerID, presetID: p.PresetID, deviceCode: reply.DeviceCode}
	return public, nil
}

func (b *Bridge) PollAuthorization(ctx context.Context, id string) (Authorization, error) {
	b.authMu.Lock()
	defer b.authMu.Unlock()
	flow := b.authFlows[id]
	if flow == nil {
		return Authorization{}, domain.Missing("authorization", id)
	}
	if time.Now().After(flow.public.ExpiresAt) {
		delete(b.authFlows, id)
		flow.public.Status = "expired"
		return flow.public, nil
	}
	if flow.public.Status == "connected" {
		return flow.public, nil
	}
	if flow.public.Status == "failed" {
		return flow.public, domain.Conflict("authorization has failed; start a new sign-in")
	}
	if time.Since(flow.lastPoll) < time.Duration(flow.public.Interval)*time.Second {
		return flow.public, nil
	}
	flow.lastPoll = time.Now()
	endpoint := "https://github.com/login/oauth/access_token"
	payload := map[string]string{"client_id": copilotClientID, "device_code": flow.deviceCode, "grant_type": "urn:ietf:params:oauth:grant-type:device_code"}
	if flow.presetID == "codex" {
		endpoint = "https://auth.openai.com/api/accounts/deviceauth/token"
		payload = map[string]string{"device_auth_id": flow.deviceCode, "user_code": flow.public.UserCode}
	}
	data, status, err := b.authRequest(ctx, endpoint, payload, false)
	if err != nil {
		return flow.public, err
	}
	if flow.presetID == "codex" && (status == 403 || status == 404) {
		return flow.public, nil
	}
	if status < 200 || status >= 300 {
		return flow.public, domain.Invalid(fmt.Sprintf("authorization failed (HTTP %d)", status))
	}
	var reply struct {
		oauthTokens
		Error    string `json:"error"`
		Code     string `json:"authorization_code"`
		Verifier string `json:"code_verifier"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return flow.public, domain.Invalid("invalid authorization response")
	}
	if reply.Error == "authorization_pending" {
		return flow.public, nil
	}
	if reply.Error == "slow_down" {
		flow.public.Interval += 5
		return flow.public, nil
	}
	if reply.Error != "" {
		flow.public.Status = "failed"
		return flow.public, domain.Invalid("provider authorization was declined or expired")
	}
	tokens := reply.oauthTokens
	if flow.presetID == "codex" {
		if reply.Code == "" || reply.Verifier == "" {
			return flow.public, domain.Invalid("authorization response is missing its code")
		}
		data, status, err = b.authRequest(ctx, "https://auth.openai.com/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {codexClientID}, "code": {reply.Code}, "code_verifier": {reply.Verifier}, "redirect_uri": {"https://auth.openai.com/deviceauth/callback"}}, true)
		if err != nil {
			return flow.public, err
		}
		if status < 200 || status >= 300 {
			return flow.public, domain.Invalid("authorization token exchange failed")
		}
		if err := json.Unmarshal(data, &tokens); err != nil {
			return flow.public, domain.Invalid("invalid token response")
		}
		tokens.ExpiresAt = time.Now().Add(time.Duration(max(60, tokens.ExpiresIn)) * time.Second)
		tokens.AccountID = accountID(tokens)
	}
	if tokens.AccessToken == "" {
		return flow.public, domain.Invalid("authorization returned no access token")
	}
	var provider domain.Provider
	err = b.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.Get(ctx, "providers", flow.providerID, &provider); err != nil {
			return err
		}
		if provider.PresetID != flow.presetID {
			return domain.Conflict("provider changed during authorization")
		}
		raw, _ := json.Marshal(tokens)
		id, err := tx.PutSecret(ctx, string(raw))
		if err != nil {
			return err
		}
		provider.CredentialID = id
		provider.ReplayRevision = provider.Revision + 1
		return tx.Save(ctx, "providers", &provider.Meta, provider)
	})
	if err != nil {
		return flow.public, err
	}
	flow.deviceCode = ""
	flow.public.Status = "connected"
	flow.public.Provider = &provider
	return flow.public, nil
}

func accountID(tokens oauthTokens) string {
	for _, token := range []string{tokens.IDToken, tokens.AccessToken} {
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			continue
		}
		data, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		var claims struct {
			ID   string `json:"chatgpt_account_id"`
			Auth struct {
				ID string `json:"chatgpt_account_id"`
			} `json:"https://api.openai.com/auth"`
		}
		if json.Unmarshal(data, &claims) == nil {
			if claims.Auth.ID != "" {
				return claims.Auth.ID
			}
			if claims.ID != "" {
				return claims.ID
			}
		}
	}
	return tokens.AccountID
}

func (b *Bridge) oauthCredentials(ctx context.Context, p domain.Provider) (oauthTokens, error) {
	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()
	var tokens oauthTokens
	value, err := b.Store.Secret(ctx, p.CredentialID)
	if err != nil {
		return tokens, err
	}
	if value == "" || json.Unmarshal([]byte(value), &tokens) != nil || tokens.AccessToken == "" {
		return tokens, domain.Conflict("sign in to this provider first")
	}
	if p.PresetID != "codex" || tokens.ExpiresAt.After(time.Now().Add(time.Minute)) {
		return tokens, nil
	}
	if tokens.RefreshToken == "" {
		return tokens, domain.Conflict("provider authorization expired; sign in again")
	}
	data, status, err := b.authRequest(ctx, "https://auth.openai.com/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {codexClientID}, "refresh_token": {tokens.RefreshToken}}, true)
	if err != nil {
		return tokens, err
	}
	if status < 200 || status >= 300 {
		if status == http.StatusBadRequest || status == http.StatusUnauthorized {
			return tokens, domain.Conflict("provider authorization expired; sign in again")
		}
		return tokens, domain.Offline(fmt.Sprintf("authorization token refresh failed (HTTP %d)", status))
	}
	var next oauthTokens
	if json.Unmarshal(data, &next) != nil || next.AccessToken == "" {
		return tokens, domain.Invalid("invalid token refresh response")
	}
	if next.RefreshToken == "" {
		next.RefreshToken = tokens.RefreshToken
	}
	next.ExpiresAt = time.Now().Add(time.Duration(max(60, next.ExpiresIn)) * time.Second)
	next.AccountID = accountID(next)
	if tokens.AccountID != "" && next.AccountID != "" && next.AccountID != tokens.AccountID {
		return tokens, domain.Conflict("authorization refresh returned a different account; sign in again")
	}
	if next.AccountID == "" {
		next.AccountID = tokens.AccountID
	}
	raw, _ := json.Marshal(next)
	if err := b.Store.RotateSecret(ctx, p.CredentialID, string(raw)); err != nil {
		return tokens, err
	}
	return next, nil
}

type oauthTransport struct {
	base     http.RoundTripper
	bridge   *Bridge
	provider domain.Provider
}

func (t oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	preset := FindPreset(t.provider.PresetID)
	if preset == nil {
		return nil, domain.Invalid("unknown authorization provider")
	}
	allowed, _ := url.Parse(preset.BaseURL)
	if req.URL.Scheme != "https" || req.URL.Host != allowed.Host {
		return nil, domain.Invalid("authorization cannot be forwarded to another host")
	}
	tokens, err := t.bridge.oauthCredentials(req.Context(), t.provider)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Del("X-Api-Key")
	clone.Header.Del("X-Goog-Api-Key")
	clone.Header.Del("Proxy-Authorization")
	clone.Header.Del("ChatGPT-Account-Id")
	clone.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	if tokens.AccountID != "" {
		clone.Header.Set("ChatGPT-Account-Id", tokens.AccountID)
	}
	clone.Header.Set("User-Agent", "Astral/0.1.0")
	if t.provider.PresetID == "codex" {
		clone.Header.Set("Originator", "astral")
	} else {
		if req.Method == http.MethodGet && req.URL.Path == "/models" {
			clone.Header.Set("X-GitHub-Api-Version", "2026-07-01")
		}
		clone.Header.Set("Copilot-Integration-Id", "astral")
	}
	return t.base.RoundTrip(clone)
}
func (b *Bridge) oauthClient(p domain.Provider) *http.Client {
	var client http.Client
	if b.HTTPClient != nil {
		client = *b.HTTPClient
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = oauthTransport{base: base, bridge: b, provider: p}
	// Even an official-host redirect can change a POST into a GET and silently
	// alter a model request. Let the caller observe the actual redirect result.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

package modelbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/oines/astral-core/models/auth"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

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
	public               Authorization
	providerID, presetID string
	state                auth.Flow
}
type oauthTokens = auth.Tokens

func (b *Bridge) oauthProtocol() *auth.Client {
	b.authMu.Lock()
	defer b.authMu.Unlock()
	if b.oauthSDK == nil {
		b.oauthSDK = &auth.Client{HTTPClient: b.HTTPClient}
	}
	return b.oauthSDK
}
func syncAuthorization(flow *authFlow) Authorization {
	flow.public.Status = flow.state.Status
	flow.public.VerificationURL = flow.state.VerificationURL
	flow.public.UserCode = flow.state.UserCode
	flow.public.Interval = flow.state.Interval
	flow.public.ExpiresAt = flow.state.ExpiresAt
	return flow.public
}
func (b *Bridge) StartAuthorization(ctx context.Context, providerID string) (Authorization, error) {
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", providerID, &provider); err != nil {
		return Authorization{}, err
	}
	state, err := b.oauthProtocol().Start(ctx, provider.PresetID)
	if err != nil {
		return Authorization{}, err
	}
	flow := &authFlow{public: Authorization{ID: domain.ID("authorization")}, providerID: providerID, presetID: provider.PresetID, state: state}
	b.authMu.Lock()
	defer b.authMu.Unlock()
	if b.authFlows == nil {
		b.authFlows = map[string]*authFlow{}
	}
	for id, old := range b.authFlows {
		if time.Now().After(old.public.ExpiresAt) || old.providerID == providerID {
			delete(b.authFlows, id)
		}
	}
	b.authFlows[flow.public.ID] = flow
	return syncAuthorization(flow), nil
}
func (b *Bridge) PollAuthorization(ctx context.Context, id string) (Authorization, error) {
	protocol := b.oauthProtocol()
	b.authMu.Lock()
	defer b.authMu.Unlock()
	flow := b.authFlows[id]
	if flow == nil {
		return Authorization{}, domain.Missing("authorization", id)
	}
	if flow.public.Status == "connected" {
		return flow.public, nil
	}
	tokens, err := protocol.Poll(ctx, &flow.state)
	syncAuthorization(flow)
	if flow.state.Status == "expired" {
		delete(b.authFlows, id)
	}
	if err != nil {
		return flow.public, err
	}
	if tokens == nil {
		return flow.public, nil
	}
	var provider domain.Provider
	err = b.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.Get(ctx, "providers", flow.providerID, &provider); err != nil {
			return err
		}
		if provider.PresetID != flow.presetID {
			return domain.Conflict("provider changed during authorization")
		}
		data, _ := json.Marshal(tokens)
		secretID, err := tx.PutSecret(ctx, string(data))
		if err != nil {
			return err
		}
		provider.CredentialID = secretID
		provider.ReplayRevision = provider.Revision + 1
		return tx.Save(ctx, "providers", &provider.Meta, provider)
	})
	if err != nil {
		flow.state.Status = "failed"
		syncAuthorization(flow)
		return flow.public, err
	}
	flow.public.Provider = &provider
	return flow.public, nil
}

type credentialStore struct {
	bridge *Bridge
	id     string
}

func (s credentialStore) Load(ctx context.Context) (auth.Tokens, error) {
	var tokens auth.Tokens
	value, err := s.bridge.Store.Secret(ctx, s.id)
	if err != nil {
		return tokens, err
	}
	if value == "" || json.Unmarshal([]byte(value), &tokens) != nil || tokens.AccessToken == "" {
		return tokens, domain.Conflict("sign in to this provider first")
	}
	return tokens, nil
}
func (s credentialStore) Save(ctx context.Context, tokens auth.Tokens) error {
	data, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	return s.bridge.Store.RotateSecret(ctx, s.id, string(data))
}
func (b *Bridge) oauthCredentials(ctx context.Context, provider domain.Provider) (auth.Tokens, error) {
	return b.oauthProtocol().Credentials(ctx, provider.PresetID, credentialStore{bridge: b, id: provider.CredentialID})
}
func (b *Bridge) oauthClient(provider domain.Provider) *http.Client {
	return b.oauthProtocol().HTTP(provider.PresetID, credentialStore{bridge: b, id: provider.CredentialID})
}

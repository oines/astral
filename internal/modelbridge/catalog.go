package modelbridge

import (
	"context"
	"encoding/json"

	"github.com/oines/astral-core/models/integration"
	"github.com/oines/astral/internal/domain"
)

type Discovery = integration.Discovery

func raw(value any) json.RawMessage { data, _ := json.Marshal(value); return data }
func (b *Bridge) providerHeaders(ctx context.Context, provider domain.Provider) (map[string]string, error) {
	value, err := b.Store.Secret(ctx, provider.HeadersCredentialID)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{}
	if value != "" {
		if err = json.Unmarshal([]byte(value), &headers); err != nil {
			return nil, domain.Invalid("invalid saved provider headers")
		}
	}
	return headers, nil
}
func (b *Bridge) Catalog(ctx context.Context, providerID string) ([]Discovery, error) {
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", providerID, &provider); err != nil {
		return nil, err
	}
	headers, err := b.providerHeaders(ctx, provider)
	if err != nil {
		return nil, err
	}
	catalog := integration.Catalog{HTTPClient: b.HTTPClient, Public: b.ModelCatalog}
	key := ""
	if preset := FindPreset(provider.PresetID); preset != nil && preset.Auth == "oauth" {
		catalog.HTTPClient = b.oauthClient(provider)
	} else {
		key, err = b.Store.Secret(ctx, provider.CredentialID)
		if err != nil {
			return nil, err
		}
	}
	return catalog.List(ctx, sdkProvider(provider), key, headers)
}
func (b *Bridge) ModelRecommendation(ctx context.Context, providerID, modelID string) (Discovery, error) {
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", providerID, &provider); err != nil {
		return Discovery{}, err
	}
	return (&integration.Catalog{Public: b.ModelCatalog}).Recommend(ctx, sdkProvider(provider), modelID)
}
func (b *Bridge) PresetCatalog(ctx context.Context, id string) ([]Discovery, error) {
	return (&integration.Catalog{Public: b.ModelCatalog}).PresetCatalog(ctx, id)
}

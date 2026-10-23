package modelbridge

import (
	"context"
	"encoding/json"
	"fmt"

	core "github.com/oines/astral-core"
	models "github.com/oines/astral-models"
	"github.com/oines/astral/internal/domain"
)

// reasoningConfig is shared by discovery of executable controls and adapter
// construction. No model-name inference is used for a custom connection.
func reasoningConfig(profile domain.ModelProfile, provider domain.Provider, values domain.ModelValues) (models.Config, string, error) {
	protocol, dialect, _, err := modelRoute(profile, provider)
	if err != nil {
		return models.Config{}, "", err
	}
	ref := Reference(profile, provider)
	config := models.Config{Model: profile.ModelID, Dialect: models.Dialect(dialect),
		Spec: core.ModelSpec{ID: ref, Revision: ref, ContextWindow: values.ContextWindow, MaxInputTokens: values.MaxInputTokens, MaxOutputTokens: values.MaxOutputTokens}}
	if len(values.Reasoning) > 0 && string(values.Reasoning) != "null" {
		config.Reasoning = &models.ReasoningCapabilities{}
		if err := json.Unmarshal(values.Reasoning, config.Reasoning); err != nil {
			return config, protocol, domain.Invalid("invalid reasoning capabilities")
		}
	}
	config.ReasoningFormat = values.ReasoningFormat
	if config.ReasoningFormat == "" {
		config.ReasoningFormat = provider.ReasoningFormat
	}
	if config.ReasoningFormat == "" && provider.PresetID != "" {
		config.ReasoningFormat = presetReasoningFormat(provider.PresetID, protocol, profile.ModelID, config.Reasoning)
	}
	// A legacy custom provider without this setting keeps its original dialect
	// behavior. New custom connections explicitly save protocol-default.
	config.ReasoningMapping = values.ReasoningMapping
	if config.ReasoningMapping == nil && config.ReasoningFormat == models.ReasoningFormatCustom {
		config.ReasoningMapping = provider.ReasoningMapping
	}
	if config.ReasoningMapping != nil && config.ReasoningFormat != models.ReasoningFormatCustom {
		return config, protocol, domain.Invalid("a reasoning parameter mapping requires the custom reasoning format")
	}
	return config, protocol, nil
}

// DescribeProfile attaches executable controls for API consumers. An invalid
// saved route is reported on that profile, without hiding other resources.
func (b *Bridge) DescribeProfile(ctx context.Context, profile domain.ModelProfile) domain.ModelProfile {
	profile.ReasoningControls, profile.ReasoningError = nil, ""
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", profile.ProviderID, &provider); err != nil {
		profile.ReasoningError = err.Error()
		return profile
	}
	values, _, err := profile.Effective()
	if err != nil {
		profile.ReasoningError = err.Error()
		return profile
	}
	config, protocol, err := reasoningConfig(profile, provider, values)
	if err == nil {
		var controls models.ReasoningControls
		controls, err = models.ResolveReasoningControls(protocol, config)
		if err == nil {
			profile.ReasoningControls = &controls
		}
	}
	if err != nil {
		profile.ReasoningError = fmt.Sprintf("reasoning settings: %s", err)
	}
	return profile
}

func (b *Bridge) ListProfiles(ctx context.Context) ([]domain.ModelProfile, error) {
	records, err := b.Store.List(ctx, "models")
	if err != nil {
		return nil, err
	}
	profiles := make([]domain.ModelProfile, 0, len(records))
	for _, record := range records {
		var profile domain.ModelProfile
		if err := json.Unmarshal(record.Data, &profile); err != nil {
			return nil, err
		}
		profiles = append(profiles, b.DescribeProfile(ctx, profile))
	}
	return profiles, nil
}

package modelbridge

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oines/astral-core/models/integration"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

// DescribeProfile attaches executable controls for API consumers. An invalid
// saved route is reported on that profile, without hiding other resources.
func (b *Bridge) DescribeProfile(ctx context.Context, profile domain.ModelProfile) domain.ModelProfile {
	profile.ReasoningControls, profile.ReasoningError = nil, ""
	available := false
	profile.Available, profile.ProviderName, profile.UnavailableReason = &available, "", ""
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", profile.ProviderID, &provider); err != nil {
		profile.UnavailableReason = "provider_missing"
		return profile
	}
	profile.ProviderName = provider.Name
	if err := rebaseProfileRecommendations(&profile, provider); err != nil {
		profile.ReasoningError = err.Error()
		profile.UnavailableReason = "invalid_configuration"
		return profile
	}
	ref := Reference(profile, provider)
	resolved, err := integration.Resolve(sdkProvider(provider), sdkModel(profile), core.ModelSpec{ID: ref, Revision: ref})
	if err == nil {
		profile.ReasoningControls = &resolved.Controls
	}
	if err != nil {
		profile.ReasoningError = fmt.Sprintf("reasoning settings: %s", err)
	}
	switch {
	case profile.Enabled != nil && !*profile.Enabled:
		profile.UnavailableReason = "model_disabled"
	case provider.Enabled != nil && !*provider.Enabled:
		profile.UnavailableReason = "provider_disabled"
	case profile.ReasoningError != "":
		profile.UnavailableReason = "invalid_configuration"
	case resolved.Values.ContextWindow <= 0 || resolved.Values.MaxOutputTokens <= 0:
		profile.UnavailableReason = "token_limits_unknown"
	default:
		available = true
	}
	return profile
}

// Availability describes whether the current selection can start a new request.
// Historical versions remain independently readable after their resource is removed.
func (b *Bridge) Availability(ctx context.Context, profileID string) (bool, string) {
	var profile domain.ModelProfile
	if err := b.Store.Get(ctx, "models", profileID, &profile); err != nil {
		return false, "model_missing"
	}
	profile = b.DescribeProfile(ctx, profile)
	return profile.Available != nil && *profile.Available, profile.UnavailableReason
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

// PreviewProfile describes an unsaved draft using its recommendation binding.
// It never saves configuration, retrieves remote metadata or invokes a model.
func (b *Bridge) PreviewProfile(ctx context.Context, profile domain.ModelProfile) domain.ModelProfile {
	return b.DescribeProfile(ctx, profile)
}

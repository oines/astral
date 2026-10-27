package modelbridge

import (
	"github.com/oines/astral-core/models/integration"
	"github.com/oines/astral/internal/domain"
)

func rebaseProfileRecommendations(profile *domain.ModelProfile, provider domain.Provider) error {
	model, err := integration.RebaseModelConfig(sdkProvider(provider), sdkModel(*profile))
	if err != nil {
		return err
	}
	profile.Recommendations, profile.RecommendationTarget = model.Recommendations, model.RecommendationTarget
	profile.RecommendationSources = integration.RebaseSources(profile.RecommendationSources, profile.Recommendations)
	if len(profile.RecommendationSources) == 0 {
		profile.RecommendationSources = nil
	}
	return nil
}

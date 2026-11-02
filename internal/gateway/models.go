package gateway

import (
	"net/http"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral/internal/httpcontract"
)

// AvailableModel exposes selection metadata without connection configuration,
// credentials or the Owner's raw recommendation and override documents.
type AvailableModel struct {
	ID                string                    `json:"id"`
	Name              string                    `json:"name"`
	ModelID           string                    `json:"model_id"`
	ProviderName      string                    `json:"provider_name"`
	ContextWindow     int                       `json:"context_window"`
	MaxInputTokens    int                       `json:"max_input_tokens,omitempty"`
	MaxOutputTokens   int                       `json:"max_output_tokens"`
	Images            string                    `json:"images"`
	DefaultReasoning  core.ReasoningOptions     `json:"default_reasoning"`
	ReasoningControls *models.ReasoningControls `json:"reasoning_controls"`
}

func (s *Service) modelRoutes() {
	register[httpcontract.Empty, []AvailableModel](s, "GET", "/v1/models", "agentModels", func(r *http.Request, _ httpcontract.Empty) ([]AvailableModel, error) {
		profiles, err := s.Harness.Models.ListProfiles(r.Context())
		if err != nil {
			return nil, err
		}
		result := make([]AvailableModel, 0, len(profiles))
		for _, profile := range profiles {
			if profile.Available == nil || !*profile.Available {
				continue
			}
			values, _, err := profile.Effective()
			if err != nil {
				continue
			}
			result = append(result, AvailableModel{
				ID: profile.ID, Name: profile.Name, ModelID: profile.ModelID, ProviderName: profile.ProviderName,
				ContextWindow: values.ContextWindow, MaxInputTokens: values.MaxInputTokens, MaxOutputTokens: values.MaxOutputTokens,
				Images: values.Images, DefaultReasoning: values.Defaults.Reasoning, ReasoningControls: profile.ReasoningControls,
			})
		}
		return result, nil
	})
}

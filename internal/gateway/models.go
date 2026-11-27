package gateway

import (
	"net/http"

	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/modelbridge"
)

type AvailableModel = modelbridge.AvailableModel

func (s *Service) modelRoutes() {
	register[httpcontract.Empty, []AvailableModel](s, "GET", "/v1/models", "agentModels", func(r *http.Request, _ httpcontract.Empty) ([]AvailableModel, error) {
		return s.Harness.Models.AvailableProfiles(r.Context())
	})
}

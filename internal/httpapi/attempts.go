package httpapi

import (
	"net/http"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/httpcontract"
)

// Model execution records have their own Owner API. Conversation history and
// reconnect snapshots do not contain model request bodies or failed outputs.
func (a *API) attemptRoutes() {
	h := a.Config.Harness
	route[httpcontract.Empty, core.AttemptPage](a, "GET", "/sessions/{id}/attempts", "sessionAttempts", "Sessions", func(r *http.Request, _ httpcontract.Empty) (core.AttemptPage, error) {
		session, err := h.Core(r.Context(), r.PathValue("id"))
		if err != nil {
			return core.AttemptPage{}, err
		}
		return session.Attempts().Scan(r.Context(), core.AttemptQuery{Cursor: r.URL.Query().Get("cursor"), Purpose: r.URL.Query().Get("purpose"), Reverse: true, Limit: limit(r, 50), MaxBytes: 8 << 20})
	}, httpcontract.Parameter{Name: "cursor"}, httpcontract.Parameter{Name: "purpose"}, httpcontract.Parameter{Name: "limit", Type: "integer"})
	route[httpcontract.Empty, core.ModelAttempt](a, "GET", "/sessions/{id}/attempts/{attempt}", "sessionAttempt", "Sessions", func(r *http.Request, _ httpcontract.Empty) (core.ModelAttempt, error) {
		session, err := h.Core(r.Context(), r.PathValue("id"))
		if err != nil {
			return core.ModelAttempt{}, err
		}
		return session.Attempts().Get(r.Context(), r.PathValue("attempt"))
	})
	route[httpcontract.Empty, core.Request](a, "GET", "/sessions/{id}/attempts/{attempt}/request", "sessionAttemptRequest", "Sessions", func(r *http.Request, _ httpcontract.Empty) (core.Request, error) {
		session, err := h.Core(r.Context(), r.PathValue("id"))
		if err != nil {
			return core.Request{}, err
		}
		return session.Attempts().Request(r.Context(), r.PathValue("attempt"))
	})
}

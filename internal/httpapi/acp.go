package httpapi

import (
	"net/http"
	"net/url"

	"github.com/oines/astral/internal/acp"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
)

type ACPTokens struct {
	Endpoint string          `json:"endpoint"`
	Tokens   []acp.TokenInfo `json:"tokens"`
}

type ACPTokenInput struct {
	Name string `json:"name"`
}

func (a *API) ACPHandler() http.Handler {
	if a.Config.ACP == nil {
		return http.NotFoundHandler()
	}
	return a.Config.ACP
}

func (a *API) acpRoutes() {
	// JSON-RPC frames are exchanged after upgrade and are described by ACP.
	a.Contract.Paths["/acp"] = map[string]any{"get": map[string]any{
		"operationId": "connectACP", "tags": []string{"ACP"},
		"description": "Upgrade to Astral WebSocket transport for ACP v1 JSON-RPC.",
		"security":    []any{map[string]any{"acpToken": []string{}}},
		"responses": map[string]any{
			"101": map[string]any{"description": "JSON-RPC WebSocket"},
			"401": map[string]any{"description": "Missing or invalid ACP token"},
			"403": map[string]any{"description": "Bot is unavailable"},
			"429": map[string]any{"description": "ACP connection limit reached"},
		},
	}}
	route[httpcontract.Empty, ACPTokens](a, "GET", "/agents/{id}/acp-tokens", "listACPTokens", "Agents", func(r *http.Request, _ httpcontract.Empty) (ACPTokens, error) {
		if a.Config.ACP == nil {
			return ACPTokens{}, domain.Offline("ACP is unavailable")
		}
		tokens, err := a.Config.ACP.ListTokens(r.Context(), r.PathValue("id"))
		return ACPTokens{Endpoint: a.acpEndpoint(r), Tokens: tokens}, err
	})
	route[ACPTokenInput, acp.CreatedToken](a, "POST", "/agents/{id}/acp-tokens", "createACPToken", "Agents", func(r *http.Request, input ACPTokenInput) (acp.CreatedToken, error) {
		if a.Config.ACP == nil {
			return acp.CreatedToken{}, domain.Offline("ACP is unavailable")
		}
		return a.Config.ACP.CreateToken(r.Context(), r.PathValue("id"), input.Name)
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/agents/{id}/acp-tokens/{token_id}", "revokeACPToken", "Agents", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		if a.Config.ACP == nil {
			return httpcontract.OK{}, domain.Offline("ACP is unavailable")
		}
		return httpcontract.OK{OK: true}, a.Config.ACP.RevokeToken(r.Context(), r.PathValue("id"), r.PathValue("token_id"))
	})
}

func (a *API) acpEndpoint(r *http.Request) string {
	origin := a.publicURL()
	if origin == "" && a.Config.RequestOrigin != nil {
		origin = a.Config.RequestOrigin(r)
	}
	target, err := url.Parse(origin)
	if err != nil || target.Host == "" || target.User != nil {
		target = &url.URL{Scheme: "http", Host: r.Host}
		if r.TLS != nil {
			target.Scheme = "https"
		}
	}
	if target.Scheme == "https" || target.Scheme == "wss" {
		target.Scheme = "wss"
	} else {
		target.Scheme = "ws"
	}
	return (&url.URL{Scheme: target.Scheme, Host: target.Host, Path: "/acp"}).String()
}

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/oines/astral/internal/botaccess"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/persistence"
)

type BotAPITokens struct {
	Endpoint string                `json:"endpoint"`
	Tokens   []botaccess.TokenInfo `json:"tokens"`
}

type BotAPITokenInput struct {
	Name string `json:"name"`
}

type botRequestKey struct{}

func botID(r *http.Request) string {
	id, _ := r.Context().Value(botRequestKey{}).(string)
	return id
}

func (a *API) authenticateBot(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Header.Get("X-Request-ID") == "" {
			r.Header.Set("X-Request-ID", domain.ID("request"))
		}
		w.Header().Set("X-Request-ID", r.Header.Get("X-Request-ID"))
		if a.botTokens == nil {
			httpcontract.Failure(w, r, domain.Offline("The Bot API is unavailable."))
			return
		}
		var header string
		if values := r.Header.Values("Authorization"); len(values) == 1 {
			header = values[0]
		}
		id, ctx, release, err := a.botTokens.Authorize(r.Context(), header)
		if errors.Is(err, botaccess.ErrUnauthorized) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			err = &domain.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "A valid Bot API bearer token is required."}
		}
		if err != nil {
			httpcontract.Failure(w, r, err)
			return
		}
		defer release()
		if ctx.Err() != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			httpcontract.Failure(w, r, &domain.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "This Bot API request has ended."})
			return
		}
		ctx = persistence.WithSessionScope(ctx, id, "")
		ctx = context.WithValue(ctx, botRequestKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *API) botTokenRoutes() {
	route[httpcontract.Empty, BotAPITokens](a, http.MethodGet, "/agents/{id}/bot-api-tokens", "listBotAPITokens", "Agents", func(r *http.Request, _ httpcontract.Empty) (BotAPITokens, error) {
		if a.botTokens == nil {
			return BotAPITokens{}, domain.Offline("The Bot API is unavailable.")
		}
		tokens, err := a.botTokens.ListTokens(r.Context(), r.PathValue("id"))
		return BotAPITokens{Endpoint: a.botAPIEndpoint(r), Tokens: tokens}, err
	})
	route[BotAPITokenInput, botaccess.CreatedToken](a, http.MethodPost, "/agents/{id}/bot-api-tokens", "createBotAPIToken", "Agents", func(r *http.Request, input BotAPITokenInput) (botaccess.CreatedToken, error) {
		if a.botTokens == nil {
			return botaccess.CreatedToken{}, domain.Offline("The Bot API is unavailable.")
		}
		return a.botTokens.CreateToken(r.Context(), r.PathValue("id"), input.Name)
	})
	route[httpcontract.Empty, httpcontract.OK](a, http.MethodDelete, "/agents/{id}/bot-api-tokens/{token_id}", "revokeBotAPIToken", "Agents", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		if a.botTokens == nil {
			return httpcontract.OK{}, domain.Offline("The Bot API is unavailable.")
		}
		err := a.botTokens.RevokeToken(r.Context(), r.PathValue("id"), r.PathValue("token_id"))
		return httpcontract.OK{OK: err == nil}, err
	})
}

func (a *API) botAPIEndpoint(r *http.Request) string {
	origin := a.publicURL()
	if origin == "" && a.Config.RequestOrigin != nil {
		origin = a.Config.RequestOrigin(r)
	}
	target, err := url.Parse(origin)
	if err != nil || target.Host == "" || target.User != nil || (target.Scheme != "http" && target.Scheme != "https") {
		target = &url.URL{Scheme: "http", Host: r.Host}
		if r.TLS != nil {
			target.Scheme = "https"
		}
	}
	return (&url.URL{Scheme: target.Scheme, Host: target.Host, Path: BotAPIPrefix}).String()
}

package httpapi

import (
	"net/http"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/serverupdate"
)

func (a *API) updater() serverupdate.Controller {
	if a.Config.Updates != nil {
		return a.Config.Updates
	}
	return serverupdate.Unmanaged{Version: a.Config.Version}
}

func (a *API) updateRoutes() {
	route[httpcontract.Empty, serverupdate.State](a, "GET", "/updates", "getServerUpdate", "System", func(r *http.Request, _ httpcontract.Empty) (serverupdate.State, error) {
		return a.updater().State(r.Context())
	})
	route[httpcontract.Empty, serverupdate.State](a, "POST", "/updates/check", "checkServerUpdate", "System", func(r *http.Request, _ httpcontract.Empty) (serverupdate.State, error) {
		if err := emptyUpdateIntent(r); err != nil {
			return serverupdate.State{}, err
		}
		return a.updater().Check(r.Context())
	})
	route[httpcontract.Empty, serverupdate.State](a, "POST", "/updates/apply", "applyServerUpdate", "System", func(r *http.Request, _ httpcontract.Empty) (serverupdate.State, error) {
		if err := emptyUpdateIntent(r); err != nil {
			return serverupdate.State{}, err
		}
		updater := a.updater()
		state, err := updater.State(r.Context())
		if err != nil {
			return state, err
		}
		if state.Phase == "unavailable" {
			return state, domain.Conflict("This installation requires a manual update.")
		}
		return updater.Apply(r.Context())
	})
}

func emptyUpdateIntent(r *http.Request) error {
	if r.ContentLength == 0 {
		return nil
	}
	var input struct{}
	return httpcontract.Decode(r, &input)
}

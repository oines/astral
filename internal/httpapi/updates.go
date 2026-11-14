package httpapi

import (
	"bufio"
	"io"
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
	if r.ContentLength < 0 {
		// A proxy can send a bodyless POST with chunked transfer encoding.
		body := bufio.NewReader(r.Body)
		if _, err := body.Peek(1); err != nil {
			if err == io.EOF {
				return nil
			}
			return domain.Invalid("invalid request body: " + err.Error())
		}
		r.Body = struct {
			io.Reader
			io.Closer
		}{body, r.Body}
	}
	var input struct{}
	return httpcontract.Decode(r, &input)
}

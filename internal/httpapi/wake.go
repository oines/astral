package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/wake"
)

func (a *API) wakeRoutes() {
	route[httpcontract.Empty, []domain.WakeEndpoint](a, "GET", "/wake-endpoints", "listWakeEndpoints", "Wake endpoints", func(r *http.Request, _ httpcontract.Empty) ([]domain.WakeEndpoint, error) {
		return a.Config.Harness.WakeEndpoints.List(r.Context(), r.URL.Query().Get("agent_id"))
	}, httpcontract.Parameter{Name: "agent_id", Description: "Omit to list endpoints for all Bots."})
	route[httpcontract.Empty, domain.WakeEndpoint](a, "GET", "/wake-endpoints/{id}", "getWakeEndpoint", "Wake endpoints", func(r *http.Request, _ httpcontract.Empty) (domain.WakeEndpoint, error) {
		return a.Config.Harness.WakeEndpoints.Get(r.Context(), "", r.PathValue("id"))
	})
	route[wake.Patch, domain.WakeEndpoint](a, "PATCH", "/wake-endpoints/{id}", "updateWakeEndpoint", "Wake endpoints", func(r *http.Request, input wake.Patch) (domain.WakeEndpoint, error) {
		return a.Config.Harness.WakeEndpoints.Update(r.Context(), "", r.PathValue("id"), input)
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/wake-endpoints/{id}", "deleteWakeEndpoint", "Wake endpoints", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		values := r.URL.Query()["revision"]
		if len(r.URL.Query()) != 1 || len(values) != 1 {
			return httpcontract.OK{}, domain.Invalid("one revision query parameter is required")
		}
		revision, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || revision <= 0 {
			return httpcontract.OK{}, domain.Invalid("revision must be a positive integer")
		}
		return httpcontract.OK{OK: true}, a.Config.Harness.WakeEndpoints.Delete(r.Context(), "", r.PathValue("id"), revision)
	}, httpcontract.Parameter{Name: "revision", Type: "integer", Required: true})
	a.Contract.Describe(httpcontract.Operation{Method: "GET", Path: "/api/v1/wake-endpoints/events", ID: "wakeEndpointEvents", Tag: "Wake endpoints", Auth: true, ContentType: "text/event-stream", Query: []httpcontract.Parameter{{Name: "agent_id"}}})
	a.mux.HandleFunc("GET /api/v1/wake-endpoints/events", a.wakeEvents)

}

func (a *API) wakeEvents(w http.ResponseWriter, r *http.Request) {
	s := a.Config.Harness.WakeEndpoints
	changes, unsubscribe := s.Subscribe()
	defer unsubscribe()
	items, err := s.List(r.Context(), r.URL.Query().Get("agent_id"))
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(items []domain.WakeEndpoint) bool {
		data, err := json.Marshal(items)
		if err != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err = fmt.Fprintf(w, "event: wake-endpoints\ndata: %s\n\n", data); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !send(items) {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-changes:
			items, err = s.List(r.Context(), r.URL.Query().Get("agent_id"))
			if err != nil || !send(items) {
				return
			}
		case <-ticker.C:
			_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if _, err = io.WriteString(w, ": keepalive\n\n"); err != nil || controller.Flush() != nil {
				return
			}
		}
	}
}

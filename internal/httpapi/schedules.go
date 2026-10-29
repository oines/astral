package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/schedule"
)

type ScheduleCreate struct {
	AgentID string `json:"agent_id"`
	schedule.Input
}

type ScheduleUpdate struct {
	Revision int64 `json:"revision"`
	schedule.Input
}

func (a *API) scheduleRoutes() {
	h := a.Config.Harness
	route[httpcontract.Empty, []domain.Schedule](a, "GET", "/schedules", "listSchedules", "Schedules", func(r *http.Request, _ httpcontract.Empty) ([]domain.Schedule, error) {
		return h.Schedules.List(r.Context(), r.URL.Query().Get("agent_id"))
	}, httpcontract.Parameter{Name: "agent_id", Description: "Omit to list schedules for all Bots."})
	route[ScheduleCreate, domain.Schedule](a, "POST", "/schedules", "createSchedule", "Schedules", func(r *http.Request, input ScheduleCreate) (domain.Schedule, error) {
		return h.Schedules.Create(r.Context(), input.AgentID, input.Input)
	})
	route[httpcontract.Empty, domain.Schedule](a, "GET", "/schedules/{id}", "getSchedule", "Schedules", func(r *http.Request, _ httpcontract.Empty) (domain.Schedule, error) {
		return h.Schedules.Get(r.Context(), "", r.PathValue("id"))
	})
	route[ScheduleUpdate, domain.Schedule](a, "PATCH", "/schedules/{id}", "updateSchedule", "Schedules", func(r *http.Request, input ScheduleUpdate) (domain.Schedule, error) {
		return h.Schedules.Update(r.Context(), "", r.PathValue("id"), input.Input, input.Revision)
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/schedules/{id}", "deleteSchedule", "Schedules", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.Schedules.Delete(r.Context(), "", r.PathValue("id"), revision(r))
	}, httpcontract.Parameter{Name: "revision", Type: "integer", Required: true})
	a.Contract.Describe(httpcontract.Operation{Method: "GET", Path: "/api/v1/schedules/events", ID: "scheduleEvents", Tag: "Schedules", Auth: true, ContentType: "text/event-stream", Query: []httpcontract.Parameter{{Name: "agent_id"}}})
	a.mux.HandleFunc("GET /api/v1/schedules/events", a.scheduleEvents)
}

func (a *API) scheduleEvents(w http.ResponseWriter, r *http.Request) {
	s := a.Config.Harness.Schedules
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
	send := func(items []domain.Schedule) bool {
		data, marshalErr := json.Marshal(items)
		if marshalErr != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err := fmt.Fprintf(w, "event: schedules\ndata: %s\n\n", data); err != nil {
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

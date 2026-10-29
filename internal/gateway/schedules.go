package gateway

import (
	"net/http"
	"strconv"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/schedule"
)

type ScheduleUpdate struct {
	Revision int64 `json:"revision"`
	schedule.Input
}

type SessionPresentation struct {
	Title    *string `json:"title,omitempty"`
	Archived *bool   `json:"archived,omitempty"`
}

func (s *Service) scheduleRoutes() {
	register[httpcontract.Empty, []domain.Schedule](s, "GET", "/v1/schedules", "agentSchedules", func(r *http.Request, _ httpcontract.Empty) ([]domain.Schedule, error) {
		return s.Harness.Schedules.List(r.Context(), binding(r).AgentID)
	})
	register[schedule.Input, domain.Schedule](s, "POST", "/v1/schedules", "agentCreateSchedule", func(r *http.Request, input schedule.Input) (domain.Schedule, error) {
		if input.SessionMode != "new" && input.SessionID == "" {
			input.SessionID = binding(r).SessionID
		}
		return s.Harness.Schedules.Create(r.Context(), binding(r).AgentID, input)
	})
	register[httpcontract.Empty, domain.Schedule](s, "GET", "/v1/schedules/{id}", "agentGetSchedule", func(r *http.Request, _ httpcontract.Empty) (domain.Schedule, error) {
		return s.Harness.Schedules.Get(r.Context(), binding(r).AgentID, r.PathValue("id"))
	})
	register[ScheduleUpdate, domain.Schedule](s, "PATCH", "/v1/schedules/{id}", "agentUpdateSchedule", func(r *http.Request, input ScheduleUpdate) (domain.Schedule, error) {
		return s.Harness.Schedules.Update(r.Context(), binding(r).AgentID, r.PathValue("id"), input.Input, input.Revision)
	})
	httpcontract.Register(s.Contract, s.mux, httpcontract.Operation{Method: "DELETE", Path: "/v1/schedules/{id}", ID: "agentDeleteSchedule", Tag: "Agent Gateway", CommandAuth: true,
		Query: []httpcontract.Parameter{{Name: "revision", Type: "integer", Required: true}}},
		func(_ http.ResponseWriter, r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
			values := r.URL.Query()["revision"]
			if len(r.URL.Query()) != 1 || len(values) != 1 {
				return httpcontract.OK{}, domain.Invalid("one revision query parameter is required")
			}
			revision, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil || revision <= 0 {
				return httpcontract.OK{}, domain.Invalid("revision must be a positive integer")
			}
			return httpcontract.OK{OK: true}, s.Harness.Schedules.Delete(r.Context(), binding(r).AgentID, r.PathValue("id"), revision)
		})
	register[SessionPresentation, httpcontract.OK](s, "PATCH", "/v1/sessions/{id}", "agentUpdateSession", func(r *http.Request, input SessionPresentation) (httpcontract.OK, error) {
		if input.Title == nil && input.Archived == nil {
			return httpcontract.OK{}, domain.Invalid("title or archived is required")
		}
		return httpcontract.OK{OK: true}, s.Harness.SaveAgentSessionPresentation(r.Context(), binding(r).AgentID, r.PathValue("id"), input.Title, input.Archived)
	})
}

package gateway

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/wake"
)

func (s *Service) wakeRoutes() {
	s.wakeContract()
	register[httpcontract.Empty, []domain.WakeEndpoint](s, "GET", "/v1/wake-endpoints", "agentWakeEndpoints", func(r *http.Request, _ httpcontract.Empty) ([]domain.WakeEndpoint, error) {
		return s.Harness.WakeEndpoints.List(r.Context(), binding(r).AgentID)
	})
	httpcontract.Register(s.Contract, s.mux, httpcontract.Operation{Method: "POST", Path: "/v1/wake-endpoints", ID: "agentCreateWakeEndpoint", Tag: "Agent Gateway", CommandAuth: true, Status: http.StatusCreated}, func(_ http.ResponseWriter, r *http.Request, input wake.CreateInput) (domain.WakeEndpoint, error) {
		return s.Harness.WakeEndpoints.Create(wake.WithComputerID(r.Context(), binding(r).ComputerID), binding(r).AgentID, input)
	})
	register[httpcontract.Empty, domain.WakeEndpoint](s, "GET", "/v1/wake-endpoints/{id}", "agentWakeEndpoint", func(r *http.Request, _ httpcontract.Empty) (domain.WakeEndpoint, error) {
		return s.Harness.WakeEndpoints.Get(r.Context(), binding(r).AgentID, r.PathValue("id"))
	})
	register[wake.Patch, domain.WakeEndpoint](s, "PATCH", "/v1/wake-endpoints/{id}", "agentUpdateWakeEndpoint", func(r *http.Request, input wake.Patch) (domain.WakeEndpoint, error) {
		return s.Harness.WakeEndpoints.Update(r.Context(), binding(r).AgentID, r.PathValue("id"), input)
	})
	register[httpcontract.Empty, httpcontract.OK](s, "DELETE", "/v1/wake-endpoints/{id}", "agentDeleteWakeEndpoint", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		values := r.URL.Query()["revision"]
		if len(r.URL.Query()) != 1 || len(values) != 1 {
			return httpcontract.OK{}, domain.Invalid("one revision query parameter is required")
		}
		revision, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || revision <= 0 {
			return httpcontract.OK{}, domain.Invalid("revision must be a positive integer")
		}
		return httpcontract.OK{OK: true}, s.Harness.WakeEndpoints.Delete(r.Context(), binding(r).AgentID, r.PathValue("id"), revision)
	}, httpcontract.Parameter{Name: "revision", Type: "integer", Required: true})
}

// HandleWake is reached only through Hub's authenticated Daemon tunnel. The
// source Computer comes from that connection, never from event JSON.
func (s *Service) HandleWake(ctx context.Context, computerID string, request computers.WakeRequest) (computers.GatewayResponse, error) {
	w := &responseWriter{header: http.Header{}}
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/hooks/event", bytes.NewReader(request.Body))
	r.Header.Set("Content-Type", "application/json")
	var event wake.Event
	_, err := s.Harness.WakeEndpoints.Check(ctx, computerID, request.EndpointID)
	if err == nil {
		err = httpcontract.Decode(r, &event)
	}
	if err == nil {
		var receipt wake.Receipt
		receipt, err = s.Harness.WakeEndpoints.Accept(ctx, computerID, request.EndpointID, event)
		if err == nil {
			httpcontract.JSON(w, http.StatusAccepted, receipt)
		}
	}
	if err != nil {
		var limit *wake.LimitError
		if errors.As(err, &limit) {
			w.header.Set("Retry-After", strconv.Itoa(max(1, limit.RetryAfter)))
			err = &domain.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: limit.Error()}
		}
		httpcontract.Failure(w, r, err)
	}
	return computers.GatewayResponse{Status: w.code, Headers: w.header, Body: w.body.Bytes()}, nil
}

// WakeContract describes the Daemon's event route alongside the management API;
// the Server itself does not expose this URL.
func (s *Service) wakeContract() {
	s.Contract.Describe(httpcontract.Operation{Method: "POST", Path: "/hooks/{id}", ID: "triggerWakeEndpoint", Tag: "Daemon events", Status: http.StatusAccepted,
		Request: reflect.TypeFor[wake.Event](), Response: reflect.TypeFor[wake.Receipt](),
		Description: "Daemon-local endpoint on the same persistent localhost port as the Computer API. No credential is required; only its owning Computer can deliver this event through its authenticated Daemon tunnel. This route is not served by the Server. JSON bodies must not exceed 64 KiB. A 202 receipt means durable acceptance; a 429 includes Retry-After and does not extend the fixed cooldown."})
}

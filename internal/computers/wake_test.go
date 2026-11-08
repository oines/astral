package computers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/computers"
)

func TestWakeRPCUsesAuthenticatedComputerNotEventClaims(t *testing.T) {
	s := fixture(t, 2)
	owner := s.daemons[0].Credentials.ComputerID
	var calls atomic.Int32
	s.hub.Wake = func(_ context.Context, computerID string, request computers.WakeRequest) (computers.GatewayResponse, error) {
		calls.Add(1)
		if request.EndpointID != "endpoint" {
			t.Error("wake RPC lost endpoint ID")
		}
		status := http.StatusAccepted
		if computerID != owner {
			status = http.StatusForbidden
		}
		raw, _ := json.Marshal(map[string]string{"source_computer_id": computerID})
		return computers.GatewayResponse{Status: status, Body: raw}, nil
	}
	for i, daemon := range s.daemons {
		if s.hub.ComputerEventURL(daemon.Credentials.ComputerID) != daemon.Hello.LocalAPIURL {
			t.Fatal("Hub did not retain actual Daemon local address")
		}
		response, err := http.Post(daemon.Hello.LocalAPIURL+"/hooks/endpoint", "application/json", strings.NewReader(`{"event_id":"one","computer_id":"`+owner+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		status := http.StatusAccepted
		if i == 1 {
			status = http.StatusForbidden
		}
		if err != nil || response.StatusCode != status || !strings.Contains(string(raw), daemon.Credentials.ComputerID) {
			t.Fatalf("event forged Computer source: status=%d body=%s err=%v", response.StatusCode, raw, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("local events did not use one RPC each")
	}
}

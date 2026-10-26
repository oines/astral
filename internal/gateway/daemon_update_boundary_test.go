package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/testkit"
)

func TestAgentGatewayCannotTriggerDaemonManagementUpdates(t *testing.T) {
	p := testkit.New(t, 1, nil)
	g := New(p.Harness, channels.New(p.Store, p.Harness))
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/computers/maintenance/" + binding.ComputerID + "/update", "/v1/management.update"} {
		response, err := g.Handle(p.Context, computers.GatewayRequest{Binding: binding, Method: "POST", Path: path, Body: json.RawMessage(`{}`)})
		if err != nil || response.Status != 404 {
			t.Fatalf("Agent acquired Daemon management endpoint: %s %d %v", path, response.Status, err)
		}
	}
	if _, err = g.Handle(p.Context, computers.GatewayRequest{Binding: binding, Method: "POST", Path: "/api/v1/computers/maintenance/" + binding.ComputerID + "/update"}); err == nil {
		t.Fatal("Agent escaped to Owner API")
	}
	raw, _ := json.Marshal(g.Contract.Document())
	if strings.Contains(string(raw), "updateComputerDaemon") || strings.Contains(string(raw), computers.DaemonUpdateCapability) {
		t.Fatal("management update leaked into Agent capabilities")
	}
}

package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestConcurrentLocalGatewaysKeepAgentSessionAndSourceIdentity(t *testing.T) {
	p := testkit.New(t, 2, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	channel := channels.New(p.Store, p.Harness)
	gateway := New(p.Harness, channel)
	p.Hub.Gateway = gateway.Handle
	other := p.Agent
	other.Meta = domain.Meta{}
	other.Name = "other Agent"
	if err := p.Harness.SaveAgent(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	otherSession, err := p.Harness.CreateSession(p.Context, other.ID, "other")
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Hub.Bind(p.Context, other.ID, otherSession.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	backend := &computers.Backend{Hub: p.Hub}
	invoke := func(b domain.ExecutionBinding, cmd string) (string, error) {
		process, err := backend.StartProcess(p.Context, core.Invocation{SessionID: b.SessionID, ExecutionID: domain.ID("execution"), Binding: computers.BindingJSON(b)}, standardtools.ProcessRequest{Command: cmd})
		if err != nil {
			return "", err
		}
		body, err := io.ReadAll(process.Output())
		_ = process.Output().Close()
		if _, waitErr := process.Wait(); err == nil {
			err = waitErr
		}
		return string(body), err
	}
	var workers sync.WaitGroup
	for _, b := range []domain.ExecutionBinding{first, second} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 4 {
				body, err := invoke(b, `curl --fail --silent "$ASTRAL_API_URL/v1/context"`)
				if err != nil {
					t.Error(err)
					return
				}
				var view Context
				if json.Unmarshal([]byte(body), &view) != nil || view.Agent.ID != b.AgentID || view.Session.ID != b.SessionID || view.SourceComputerID != b.ComputerID {
					t.Errorf("crossed execution context: %s", body)
				}
			}
		}()
	}
	workers.Wait()
	if err = p.Harness.SelectComputer(p.Context, p.Session.ID, p.Daemons[1].Credentials.ComputerID, ""); err != nil {
		t.Fatal(err)
	}
	body, err := invoke(first, `curl --fail --silent "$ASTRAL_API_URL/v1/context"`)
	if err != nil {
		t.Fatal(err)
	}
	var view Context
	_ = json.Unmarshal([]byte(body), &view)
	if view.SourceComputerID != first.ComputerID || view.Session.ComputerID == first.ComputerID {
		t.Fatal("old process Gateway was rebound to current Computer")
	}
	reply, err := gateway.Handle(p.Context, computers.GatewayRequest{Binding: first, Method: "GET", Path: "/v1/sessions/" + otherSession.ID + "/history"})
	if err != nil || reply.Status != http.StatusForbidden {
		t.Fatalf("cross-Agent history visible: %+v %v", reply, err)
	}
	reply, err = gateway.Handle(p.Context, computers.GatewayRequest{Binding: first, Method: "POST", Path: "/v1/im/messages", Body: json.RawMessage(`{"text":"forged","agent_id":"other"}`), Intent: "forged"})
	if err != nil || reply.Status != 400 {
		t.Fatalf("forged identity accepted: %+v %v", reply, err)
	}
	reply, err = gateway.Handle(p.Context, computers.GatewayRequest{Binding: first, Method: "POST", Path: "/v1/im/messages", Body: json.RawMessage(`{"text":"no implicit target"}`), Intent: "no-target"})
	if err != nil || reply.Status != 400 || !strings.Contains(string(reply.Body), "no default external destination") {
		t.Fatalf("Web Session acquired an implicit IM destination: %+v %v", reply, err)
	}
	forged := first
	forged.AgentID = other.ID
	if _, err = gateway.Handle(p.Context, computers.GatewayRequest{Binding: forged, Method: "GET", Path: "/v1/context"}); err == nil {
		t.Fatal("forged internal grant accepted")
	}
	p.Agent.AllowedComputers = []string{p.Daemons[1].Credentials.ComputerID}
	p.Agent.DefaultComputerID = p.Daemons[1].Credentials.ComputerID
	if err = p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
		t.Fatal(err)
	}
	if _, err = gateway.Handle(p.Context, computers.GatewayRequest{Binding: first, Method: "GET", Path: "/v1/context"}); err == nil {
		t.Fatal("revoked source retained API access")
	}
}

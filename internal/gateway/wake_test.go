package gateway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/testkit"
	"github.com/oines/astral/internal/wake"
)

func TestWakeEndpointsManagementAndComputerBoundAnonymousEvents(t *testing.T) {
	f := newScheduleGateway(t, 2)
	p := f.p
	var endpoint domain.WakeEndpoint
	f.call(t, false, "POST", "/v1/wake-endpoints", wake.CreateInput{Title: "Listener", Prompt: "Check the deployment result", Notes: "/opt/jobs/listener.py"}, &endpoint, http.StatusCreated)
	if endpoint.SessionID != p.Session.ID || endpoint.SourceSessionID != p.Session.ID || endpoint.ComputerID != p.Daemons[0].Credentials.ComputerID || endpoint.TriggerURL != p.Daemons[0].Hello.LocalAPIURL+"/hooks/"+endpoint.ID {
		t.Fatalf("creation did not preserve trusted source and default target: %+v", endpoint)
	}
	var read domain.WakeEndpoint
	f.call(t, true, "GET", "/api/v1/wake-endpoints/"+endpoint.ID, nil, &read, 200)
	if read.ID != endpoint.ID || read.Notes != endpoint.Notes {
		t.Fatal("Owner did not see Agent-created endpoint")
	}
	f.call(t, true, "POST", "/api/v1/wake-endpoints", wake.CreateInput{Title: "Forbidden owner creation"}, nil, http.StatusMethodNotAllowed)
	for _, field := range []string{"agent_id", "computer_id", "source_session_id"} {
		f.call(t, false, "POST", "/v1/wake-endpoints", map[string]any{"title": "Forged source", field: "forged"}, nil, http.StatusBadRequest)
	}
	post := func(url, body string, status int) {
		t.Helper()
		response, err := http.Post(url, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		if response.StatusCode != status {
			t.Fatalf("event status=%d body=%s", response.StatusCode, raw)
		}
	}
	post(endpoint.TriggerURL, `{"event_id":"one","text":"deployment done"}`, http.StatusAccepted)
	post(endpoint.TriggerURL, `{"event_id":"one","text":"deployment done"}`, http.StatusAccepted)
	post(p.Daemons[1].Hello.LocalAPIURL+"/hooks/"+endpoint.ID, `{"event_id":"foreign","text":"must not arrive"}`, http.StatusForbidden)
	post(endpoint.TriggerURL, `{"event_id":"forged","session_id":"other"}`, http.StatusBadRequest)
	serverRequest, err := http.NewRequestWithContext(p.Context, "POST", f.ownerURL+"/hooks/"+endpoint.ID, strings.NewReader(`{"event_id":"server"}`))
	if err != nil {
		t.Fatal(err)
	}
	serverRequest.Header.Set("Authorization", "Desktop schedule-owner-key")
	response, err := f.owner.Client().Do(serverRequest)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("Server exposed a public event trigger")
	}
	secondBinding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Daemons[1].Credentials.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	secondClient := testkit.ComputerAPI(t, p, secondBinding)
	request, err := http.NewRequestWithContext(p.Context, "GET", secondBinding.APIURL+"/v1/wake-endpoints/"+endpoint.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = secondClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&read) != nil || read.ComputerID != endpoint.ComputerID {
		t.Fatal("other authorized Computer could not manage endpoint without changing source")
	}
	notes := "/opt/jobs/listener.py, systemd: deploy-events"
	f.call(t, true, "PATCH", "/api/v1/wake-endpoints/"+endpoint.ID, wake.Patch{Revision: endpoint.Revision, Notes: &notes}, &endpoint, 200)
	f.call(t, true, "PATCH", "/api/v1/wake-endpoints/"+endpoint.ID, map[string]any{"revision": endpoint.Revision, "session_id": p.Session.ID}, nil, 400)
	f.call(t, true, "PATCH", "/api/v1/wake-endpoints/"+endpoint.ID, map[string]any{"revision": endpoint.Revision, "computer_id": p.Daemons[1].Credentials.ComputerID}, nil, 400)
	for _, suffix := range []string{"/credential", "/credential/rotate"} {
		f.call(t, false, "GET", "/v1/wake-endpoints/"+endpoint.ID+suffix, nil, nil, 404)
	}
	paused := false
	f.call(t, true, "PATCH", "/api/v1/wake-endpoints/"+endpoint.ID, wake.Patch{Revision: endpoint.Revision, Enabled: &paused}, &endpoint, 200)
	post(endpoint.TriggerURL, `{"event_id":"paused","text":"must not arrive"}`, 409)
	f.call(t, false, "DELETE", "/v1/wake-endpoints/"+endpoint.ID+"?revision="+strconv.FormatInt(endpoint.Revision, 10), nil, nil, 200)
	post(endpoint.TriggerURL, `{"event_id":"deleted","text":"must not arrive"}`, 404)
	if p.Requests.Load() != 0 {
		t.Fatal("management or event admission invoked a model before the worker started")
	}
}

func TestWakeEndpointTargetsPrivacyAndSubagentManagement(t *testing.T) {
	f := newScheduleGateway(t)
	p := f.p
	other, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "Explicit target")
	if err != nil {
		t.Fatal(err)
	}
	var endpoint domain.WakeEndpoint
	f.call(t, false, "POST", "/v1/wake-endpoints", wake.CreateInput{Title: "Explicit", SessionID: other.ID}, &endpoint, 201)
	if endpoint.SessionID != other.ID {
		t.Fatal("explicit target was ignored")
	}
	other.Private = true
	if err = p.Store.Save(p.Context, "sessions", &other.Meta, &other); err != nil {
		t.Fatal(err)
	}
	f.call(t, false, "GET", "/v1/wake-endpoints/"+endpoint.ID, nil, nil, 404)
	f.call(t, false, "POST", "/v1/wake-endpoints", wake.CreateInput{Title: "Private", SessionID: other.ID}, nil, 404)
	child, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "Child")
	if err != nil {
		t.Fatal(err)
	}
	child.SubagentParentID = p.Session.ID
	if err = p.Store.Save(p.Context, "sessions", &child.Meta, &child); err != nil {
		t.Fatal(err)
	}
	f.call(t, false, "POST", "/v1/wake-endpoints", wake.CreateInput{Title: "Invalid child target", SessionID: child.ID}, nil, 403)
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, child.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := testkit.ComputerAPI(t, p, binding)
	for _, method := range []string{"GET", "POST"} {
		request, err := http.NewRequestWithContext(p.Context, method, binding.APIURL+"/v1/wake-endpoints", strings.NewReader(`{"title":"child cannot create"}`))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatalf("child %s endpoint management status=%d", method, response.StatusCode)
		}
	}
}

func TestWakeEndpointDiscoveryAndOwnerSubscription(t *testing.T) {
	f := newScheduleGateway(t)
	var doc gateway.CapabilityDocument
	f.call(t, false, "GET", "/v1/capabilities/wake-endpoints", nil, &doc, 200)
	for _, term := range []string{"notes", "local", "60-second", "20 events", "revision", "session_id", "Do not expose or forward hooks endpoints to the public internet", "reverse proxies", "webhook relay services"} {
		if !strings.Contains(strings.Join(doc.Notes, " "), term) {
			t.Fatalf("missing discovery rule %s", term)
		}
	}
	paths := doc.OpenAPI["paths"].(map[string]any)
	if paths["/v1/wake-endpoints"] == nil || paths["/v1/wake-endpoints/{id}"] == nil {
		t.Fatal("wake discovery omitted management routes")
	}
	ctx, cancel := context.WithTimeout(f.p.Context, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", f.ownerURL+"/api/v1/wake-endpoints/events?agent_id="+f.p.Agent.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Desktop schedule-owner-key")
	response, err := f.owner.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	read := func() []domain.WakeEndpoint {
		t.Helper()
		for scanner.Scan() {
			if raw, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				var endpoints []domain.WakeEndpoint
				if json.Unmarshal([]byte(raw), &endpoints) != nil {
					t.Fatal(raw)
				}
				return endpoints
			}
		}
		t.Fatalf("wake stream ended: %v", scanner.Err())
		return nil
	}
	if len(read()) != 0 {
		t.Fatal("initial wake list was not empty")
	}
	var endpoint domain.WakeEndpoint
	f.call(t, false, "POST", "/v1/wake-endpoints", wake.CreateInput{Title: "Streamed"}, &endpoint, 201)
	if items := read(); len(items) != 1 || items[0].ID != endpoint.ID {
		t.Fatal("Agent creation was not broadcast to Owner")
	}
	cancel()
}

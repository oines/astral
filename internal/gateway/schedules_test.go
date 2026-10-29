package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/httpapi"
	"github.com/oines/astral/internal/schedule"
	"github.com/oines/astral/internal/testkit"
)

type scheduleGatewayFixture struct {
	p                  *testkit.Platform
	agentClient        *http.Client
	agentURL, ownerURL string
	owner              *httptest.Server
}

func newScheduleGateway(t *testing.T) *scheduleGatewayFixture {
	t.Helper()
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	c := channels.New(p.Store, p.Harness)
	g := gateway.New(p.Harness, c)
	p.Hub.Gateway = g.Handle
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := testkit.ComputerAPI(t, p, binding)
	authentication, err := auth.New(p.Store, &auth.Config{DesktopToken: "schedule-owner-key"})
	if err != nil {
		t.Fatal(err)
	}
	owner := httptest.NewServer(httpapi.New(httpapi.Config{Harness: p.Harness, Channels: c, Auth: authentication, DataDirectory: p.Directory, Version: "test"}))
	t.Cleanup(owner.Close)
	return &scheduleGatewayFixture{p: p, agentClient: client, agentURL: binding.APIURL, ownerURL: owner.URL, owner: owner}
}

func (f *scheduleGatewayFixture) call(t *testing.T, owner bool, method, path string, input, output any, status int) {
	t.Helper()
	base, client := f.agentURL, f.agentClient
	if owner {
		base, client = f.ownerURL, f.owner.Client()
	}
	var body []byte
	if input != nil {
		body = testkit.JSON(input)
	}
	request, err := http.NewRequestWithContext(f.p.Context, method, base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if owner {
		request.Header.Set("Authorization", "Desktop schedule-owner-key")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != status {
		t.Fatalf("owner=%v %s %s: status=%d body=%s error=%v", owner, method, path, response.StatusCode, raw, err)
	}
	if output != nil {
		target := reflect.ValueOf(output).Elem()
		target.Set(reflect.Zero(target.Type()))
		if err := json.Unmarshal(raw, output); err != nil {
			t.Fatal(err)
		}
	}
}

func scheduleInput(sessionID, title string) schedule.Input {
	return schedule.Input{SessionID: sessionID, Title: title, Prompt: "Review the project and report meaningful changes.", Enabled: true,
		Timing: domain.ScheduleTiming{Kind: "cron", Cron: "0 9 * * *"}}
}

func scheduleRevision(plan domain.Schedule) string { return strconv.FormatInt(plan.Revision, 10) }

func assertScheduleTargetMode(t *testing.T, plan domain.Schedule, mode, sessionID string) {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(testkit.JSON(plan), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["session_mode"] != mode || plan.SessionID != sessionID {
		t.Fatalf("unexpected schedule target: mode=%v session=%q, want mode=%q session=%q", fields["session_mode"], plan.SessionID, mode, sessionID)
	}
	if mode == "new" && fields["last_session_id"] != nil && fields["last_session_id"] != "" {
		t.Fatal("a never-executed new-Session plan already retained a target Session")
	}
}

func TestGatewayNewSessionSchedulesShareOwnerStateAndSwitchTargetModes(t *testing.T) {
	f := newScheduleGateway(t)
	p := f.p
	second, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "Existing target")
	if err != nil {
		t.Fatal(err)
	}
	newPlan := map[string]any{
		"session_mode": "new", "title": "Fresh conversation each morning", "prompt": "Review the project.", "enabled": true,
		"timing": map[string]any{"kind": "cron", "cron": "0 9 * * *"},
	}
	var fromAgent, fromOwner, read domain.Schedule
	// In new mode the caller Session must never be inserted as the target.
	f.call(t, false, "POST", "/v1/schedules", newPlan, &fromAgent, http.StatusOK)
	assertScheduleTargetMode(t, fromAgent, "new", "")
	f.call(t, true, "GET", "/api/v1/schedules/"+fromAgent.ID, nil, &read, http.StatusOK)
	assertScheduleTargetMode(t, read, "new", "")
	ownerBody := map[string]any{}
	for key, value := range newPlan {
		ownerBody[key] = value
	}
	ownerBody["agent_id"], ownerBody["title"] = p.Agent.ID, "Owner's fresh conversation plan"
	f.call(t, true, "POST", "/api/v1/schedules", ownerBody, &fromOwner, http.StatusOK)
	f.call(t, false, "GET", "/v1/schedules/"+fromOwner.ID, nil, &read, http.StatusOK)
	assertScheduleTargetMode(t, read, "new", "")
	if read.ID != fromOwner.ID || read.Title != fromOwner.Title || read.AgentID != p.Agent.ID {
		t.Fatal("Owner-created new-Session plan was not visible through Gateway")
	}
	update := map[string]any{
		"revision": fromAgent.Revision, "session_mode": "existing", "session_id": second.ID,
		"title": "Owner retargeted to an existing conversation", "prompt": newPlan["prompt"], "enabled": true, "timing": newPlan["timing"],
	}
	f.call(t, true, "PATCH", "/api/v1/schedules/"+fromAgent.ID, update, &fromAgent, http.StatusOK)
	f.call(t, false, "GET", "/v1/schedules/"+fromAgent.ID, nil, &read, http.StatusOK)
	assertScheduleTargetMode(t, read, "existing", second.ID)
	update["revision"], update["session_mode"], update["title"] = fromAgent.Revision, "new", "Agent switched back to a fresh conversation"
	delete(update, "session_id")
	f.call(t, false, "PATCH", "/v1/schedules/"+fromAgent.ID, update, &fromAgent, http.StatusOK)
	f.call(t, true, "GET", "/api/v1/schedules/"+fromAgent.ID, nil, &read, http.StatusOK)
	assertScheduleTargetMode(t, read, "new", "")
	if read.Title != update["title"] || read.Revision != fromAgent.Revision {
		t.Fatal("Gateway target-mode edit was not visible to the Owner")
	}
	// The other direction uses the Gateway to edit an Owner-created plan,
	// then the Owner switches it back without an obsolete session_id.
	update["revision"], update["session_mode"], update["session_id"] = fromOwner.Revision, "existing", p.Session.ID
	f.call(t, false, "PATCH", "/v1/schedules/"+fromOwner.ID, update, &fromOwner, http.StatusOK)
	f.call(t, true, "GET", "/api/v1/schedules/"+fromOwner.ID, nil, &read, http.StatusOK)
	assertScheduleTargetMode(t, read, "existing", p.Session.ID)
	update["revision"], update["session_mode"] = fromOwner.Revision, "new"
	delete(update, "session_id")
	f.call(t, true, "PATCH", "/api/v1/schedules/"+fromOwner.ID, update, &fromOwner, http.StatusOK)
	f.call(t, false, "GET", "/v1/schedules/"+fromOwner.ID, nil, &read, http.StatusOK)
	assertScheduleTargetMode(t, read, "new", "")
	invalid := map[string]any{}
	for key, value := range newPlan {
		invalid[key] = value
	}
	invalid["session_id"] = p.Session.ID
	f.call(t, false, "POST", "/v1/schedules", invalid, nil, http.StatusBadRequest)
	invalid["agent_id"] = p.Agent.ID
	f.call(t, true, "POST", "/api/v1/schedules", invalid, nil, http.StatusBadRequest)
	delete(invalid, "agent_id")
	invalid["revision"] = fromAgent.Revision
	f.call(t, false, "PATCH", "/v1/schedules/"+fromAgent.ID, invalid, nil, http.StatusBadRequest)
	f.call(t, true, "PATCH", "/api/v1/schedules/"+fromAgent.ID, invalid, nil, http.StatusBadRequest)
	delete(invalid, "revision")
	delete(invalid, "session_id")
	invalid["agent_id"] = "forged-bot-identity"
	f.call(t, false, "POST", "/v1/schedules", invalid, nil, http.StatusBadRequest)
	invalid["revision"] = fromAgent.Revision
	f.call(t, false, "PATCH", "/v1/schedules/"+fromAgent.ID, invalid, nil, http.StatusBadRequest)
	f.call(t, false, "GET", "/v1/schedules/"+fromAgent.ID, nil, &read, http.StatusOK)
	assertScheduleTargetMode(t, read, "new", "")
	if read.Revision != fromAgent.Revision || p.Requests.Load() != 0 {
		t.Fatal("invalid new-Session edits changed the plan or invoked a model")
	}
}

func TestGatewaySchedulesShareOwnerStateAndEnforceBotScope(t *testing.T) {
	f := newScheduleGateway(t)
	p := f.p
	second, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "same Bot target")
	if err != nil {
		t.Fatal(err)
	}
	other := p.Agent
	other.Meta = domain.Meta{}
	if err := p.Harness.SaveAgent(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	foreign, err := p.Harness.CreateSession(p.Context, other.ID, "foreign target")
	if err != nil {
		t.Fatal(err)
	}
	// Omitting the target resolves to the command's calling Session, without
	// accepting an agent_id from the request body.
	input := scheduleInput("", "Agent-created plan")
	var agentPlan domain.Schedule
	f.call(t, false, "POST", "/v1/schedules", input, &agentPlan, http.StatusOK)
	if agentPlan.AgentID != p.Agent.ID || agentPlan.SessionID != p.Session.ID || agentPlan.NextRunAt == nil {
		t.Fatalf("creation did not use the live command identity: %+v", agentPlan)
	}
	var ownerRead domain.Schedule
	f.call(t, true, "GET", "/api/v1/schedules/"+agentPlan.ID, nil, &ownerRead, http.StatusOK)
	if ownerRead.ID != agentPlan.ID || ownerRead.Prompt != input.Prompt || ownerRead.Revision != agentPlan.Revision {
		t.Fatal("Gateway-created plan was not visible to the Owner")
	}
	var ownerPlan domain.Schedule
	f.call(t, true, "POST", "/api/v1/schedules", httpapi.ScheduleCreate{AgentID: p.Agent.ID, Input: scheduleInput(second.ID, "Owner-created plan")}, &ownerPlan, http.StatusOK)
	var agentRead domain.Schedule
	f.call(t, false, "GET", "/v1/schedules/"+ownerPlan.ID, nil, &agentRead, http.StatusOK)
	if agentRead.ID != ownerPlan.ID || agentRead.SessionID != second.ID {
		t.Fatal("Owner-created plan was not visible through Gateway")
	}
	paused := scheduleInput(second.ID, "Owner edited and paused")
	paused.Enabled = false
	f.call(t, true, "PATCH", "/api/v1/schedules/"+ownerPlan.ID, httpapi.ScheduleUpdate{Revision: ownerPlan.Revision, Input: paused}, &ownerPlan, http.StatusOK)
	f.call(t, false, "GET", "/v1/schedules/"+ownerPlan.ID, nil, &agentRead, http.StatusOK)
	if agentRead.Enabled || agentRead.NextRunAt != nil || agentRead.Title != paused.Title || agentRead.Revision != ownerPlan.Revision {
		t.Fatal("Owner edit did not become the Gateway's current plan")
	}
	previous := agentPlan
	edit := scheduleInput(second.ID, "Agent retargeted plan")
	f.call(t, false, "PATCH", "/v1/schedules/"+agentPlan.ID, gateway.ScheduleUpdate{Revision: previous.Revision, Input: edit}, &agentPlan, http.StatusOK)
	f.call(t, true, "GET", "/api/v1/schedules/"+agentPlan.ID, nil, &ownerRead, http.StatusOK)
	if ownerRead.Title != edit.Title || ownerRead.SessionID != second.ID || ownerRead.Revision <= previous.Revision {
		t.Fatal("Gateway edit was not visible to the Owner")
	}
	f.call(t, false, "PATCH", "/v1/schedules/"+agentPlan.ID, gateway.ScheduleUpdate{Revision: previous.Revision, Input: edit}, nil, http.StatusConflict)
	f.call(t, false, "DELETE", "/v1/schedules/"+agentPlan.ID+"?revision="+scheduleRevision(previous), nil, nil, http.StatusConflict)
	for _, suffix := range []string{"", "?revision=bad", "?revision=0", "?revision=1&revision=2", "?revision=1&agent_id=" + other.ID} {
		f.call(t, false, "DELETE", "/v1/schedules/"+agentPlan.ID+suffix, nil, nil, http.StatusBadRequest)
	}
	var foreignPlan domain.Schedule
	f.call(t, true, "POST", "/api/v1/schedules", httpapi.ScheduleCreate{AgentID: other.ID, Input: scheduleInput(foreign.ID, "Foreign plan")}, &foreignPlan, http.StatusOK)
	var plans []domain.Schedule
	f.call(t, false, "GET", "/v1/schedules", nil, &plans, http.StatusOK)
	if len(plans) != 2 {
		t.Fatalf("same-Bot list omitted or exposed plans: %+v", plans)
	}
	for _, plan := range plans {
		if plan.AgentID != p.Agent.ID {
			t.Fatal("Gateway list exposed another Bot's plan")
		}
	}
	f.call(t, false, "GET", "/v1/schedules/"+foreignPlan.ID, nil, nil, http.StatusNotFound)
	f.call(t, false, "PATCH", "/v1/schedules/"+foreignPlan.ID, gateway.ScheduleUpdate{Revision: foreignPlan.Revision, Input: scheduleInput(foreign.ID, "stolen")}, nil, http.StatusNotFound)
	f.call(t, false, "DELETE", "/v1/schedules/"+foreignPlan.ID+"?revision="+scheduleRevision(foreignPlan), nil, nil, http.StatusNotFound)
	f.call(t, false, "POST", "/v1/schedules", scheduleInput(foreign.ID, "Foreign target denied"), nil, http.StatusNotFound)
	forged := map[string]any{"agent_id": other.ID, "session_id": foreign.ID, "title": "forged", "prompt": "forged", "enabled": true, "timing": input.Timing}
	f.call(t, false, "POST", "/v1/schedules", forged, nil, http.StatusBadRequest)
	forged["revision"] = agentPlan.Revision
	f.call(t, false, "PATCH", "/v1/schedules/"+agentPlan.ID, forged, nil, http.StatusBadRequest)
	f.call(t, false, "DELETE", "/v1/schedules/"+agentPlan.ID+"?revision="+scheduleRevision(agentPlan), nil, nil, http.StatusOK)
	f.call(t, true, "GET", "/api/v1/schedules/"+agentPlan.ID, nil, nil, http.StatusNotFound)
	f.call(t, true, "DELETE", "/api/v1/schedules/"+ownerPlan.ID+"?revision="+scheduleRevision(ownerPlan), nil, nil, http.StatusOK)
	f.call(t, false, "GET", "/v1/schedules/"+ownerPlan.ID, nil, nil, http.StatusNotFound)
	if p.Requests.Load() != 0 {
		t.Fatal("managing plans unexpectedly invoked a model")
	}
}

func TestGatewaySessionPresentationRespectsOwnershipAndIMBinding(t *testing.T) {
	f := newScheduleGateway(t)
	p := f.p
	target, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "Original title")
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/sessions/" + target.ID
	f.call(t, false, "PATCH", path, map[string]any{"title": "Agent manual title"}, nil, http.StatusOK)
	f.call(t, false, "PATCH", path, map[string]any{"archived": true}, nil, http.StatusOK)
	if err := p.Store.Get(p.Context, "sessions", target.ID, &target); err != nil || target.Title != "Agent manual title" || target.TitleSource != domain.TitleManual || !target.Archived {
		t.Fatalf("Gateway did not rename/archive the same-Bot Session: %+v %v", target, err)
	}
	f.call(t, false, "PATCH", path, map[string]any{"archived": false}, nil, http.StatusOK)
	for _, input := range []any{map[string]any{"pinned": true}, map[string]any{"title": "forged pin", "pinned": true}, map[string]any{"title": " "}, map[string]any{}, map[string]any{"agent_id": p.Agent.ID, "title": "forged identity"}} {
		f.call(t, false, "PATCH", path, input, nil, http.StatusBadRequest)
	}
	other := p.Agent
	other.Meta = domain.Meta{}
	if err := p.Harness.SaveAgent(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	foreign, err := p.Harness.CreateSession(p.Context, other.ID, "Foreign title")
	if err != nil {
		t.Fatal(err)
	}
	f.call(t, false, "PATCH", "/v1/sessions/"+foreign.ID, map[string]any{"title": "stolen", "archived": true}, nil, http.StatusForbidden)
	// A current binding is the guard; the Session's historical creation source
	// does not grant a later right to rename its IM chat title.
	if err := p.Store.Get(p.Context, "sessions", target.ID, &target); err != nil {
		t.Fatal(err)
	}
	if target.Archived || target.Pinned || target.Title != "Agent manual title" {
		t.Fatal("restore or rejected edits changed unrelated presentation")
	}
	target.BindingID = "current-im-binding"
	if err := p.Store.Save(p.Context, "sessions", &target.Meta, &target); err != nil {
		t.Fatal(err)
	}
	for _, input := range []any{map[string]any{"title": "forbidden rename"}, map[string]any{"archived": true}, map[string]any{"archived": false}} {
		f.call(t, false, "PATCH", path, input, nil, http.StatusConflict)
		f.call(t, true, "PATCH", "/api/v1/sessions/"+target.ID, input, nil, http.StatusConflict)
	}
	if err := p.Store.Get(p.Context, "sessions", target.ID, &target); err != nil || target.Title != "Agent manual title" || target.Archived {
		t.Fatalf("rejected IM-bound edits changed the Session: %+v %v", target, err)
	}
	if p.Requests.Load() != 0 {
		t.Fatal("presentation changes invoked a model")
	}
}

func TestGatewayCapabilitiesDescribeScheduleAndSessionManagement(t *testing.T) {
	f := newScheduleGateway(t)
	var index gateway.Capabilities
	f.call(t, false, "GET", "/v1/capabilities", nil, &index, http.StatusOK)
	urls := map[string]string{}
	for _, capability := range index.Capabilities {
		urls[capability.ID] = capability.ReadURL
	}
	for id, expected := range map[string]map[string][]string{
		"schedules": {"/v1/schedules": {"get", "post"}, "/v1/schedules/{id}": {"get", "patch", "delete"}},
		"sessions":  {"/v1/sessions/{id}": {"patch"}},
	} {
		if urls[id] == "" {
			t.Fatalf("capability %s is missing from the discovery index", id)
		}
		var doc gateway.CapabilityDocument
		f.call(t, false, "GET", urls[id], nil, &doc, http.StatusOK)
		paths := doc.OpenAPI["paths"].(map[string]any)
		for path, methods := range expected {
			operations, ok := paths[path].(map[string]any)
			if !ok {
				t.Fatalf("%s discovery omitted path %s", id, path)
			}
			for _, method := range methods {
				if operations[method] == nil {
					t.Fatalf("%s discovery omitted %s %s", id, method, path)
				}
			}
		}
		if id == "schedules" {
			notes := strings.Join(doc.Notes, " ")
			for _, term := range []string{"Steer", "revision", "skipped", "WebUI", "send_message", "session_mode=new", "last_session_id"} {
				if !strings.Contains(notes, term) {
					t.Fatalf("schedule discovery omitted behavior %q", term)
				}
			}
		}
	}
}

func TestGatewayScheduleMutationMethodsRetainCommandAuthenticationAndOriginChecks(t *testing.T) {
	f := newScheduleGateway(t)
	for _, method := range []string{"PATCH", "DELETE"} {
		for _, browser := range []bool{false, true} {
			request, err := http.NewRequestWithContext(f.p.Context, method, f.agentURL+"/v1/schedules/missing", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			client, expected := http.DefaultClient, http.StatusUnauthorized
			if browser {
				request.Header.Set("Origin", "https://untrusted.invalid")
				client, expected = f.agentClient, http.StatusForbidden
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != expected {
				t.Fatalf("%s browser=%v bypassed the local Gateway boundary: %d", method, browser, response.StatusCode)
			}
		}
	}
}

func TestGatewayScheduleWriteBroadcastsOwnerSnapshot(t *testing.T) {
	f := newScheduleGateway(t)
	ctx, cancel := context.WithTimeout(f.p.Context, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", f.ownerURL+"/api/v1/schedules/events?agent_id="+f.p.Agent.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Desktop schedule-owner-key")
	response, err := f.owner.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("schedule subscription rejected: %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	readSnapshot := func() []domain.Schedule {
		t.Helper()
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var plans []domain.Schedule
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &plans); err != nil {
				t.Fatal(err)
			}
			return plans
		}
		t.Fatalf("schedule event stream ended before a snapshot: %v", scanner.Err())
		return nil
	}
	if plans := readSnapshot(); len(plans) != 0 {
		t.Fatalf("initial snapshot was not current empty state: %+v", plans)
	}
	var created domain.Schedule
	f.call(t, false, "POST", "/v1/schedules", scheduleInput("", "Created through Gateway"), &created, http.StatusOK)
	if plans := readSnapshot(); len(plans) != 1 || plans[0].ID != created.ID || plans[0].Title != created.Title {
		t.Fatalf("Gateway write did not reach the Owner subscription: %+v", plans)
	}
	cancel()
	_ = response.Body.Close()
}

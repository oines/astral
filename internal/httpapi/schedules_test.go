package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/schedule"
	"github.com/oines/astral/internal/testkit"
)

func scheduleOwnerCall(t *testing.T, p *testkit.Platform, server *httptest.Server, method, path string, input, output any, status int) {
	t.Helper()
	var body []byte
	if input != nil {
		body = testkit.JSON(input)
	}
	response := request(t, p.Context, server, method, path, bytes.NewReader(body), true)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != status {
		t.Fatalf("%s %s: status=%d body=%s error=%v", method, path, response.StatusCode, raw, err)
	}
	if output != nil {
		target := reflect.ValueOf(output).Elem()
		target.Set(reflect.Zero(target.Type()))
		if err := json.Unmarshal(raw, output); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOwnerScheduleCRUDAndRevisionConflicts(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	input := schedule.Input{SessionID: p.Session.ID, Title: "One-time reminder", Prompt: "Check the deployment result.", Enabled: true, Timing: domain.ScheduleTiming{Kind: "once", At: &at}}
	var created domain.Schedule
	scheduleOwnerCall(t, p, server, "POST", "/api/v1/schedules", ScheduleCreate{AgentID: p.Agent.ID, Input: input}, &created, http.StatusOK)
	if created.ID == "" || created.Revision <= 0 || created.AgentID != p.Agent.ID || created.NextRunAt == nil || !created.NextRunAt.Equal(at) {
		t.Fatalf("invalid created plan: %+v", created)
	}
	var read domain.Schedule
	scheduleOwnerCall(t, p, server, "GET", "/api/v1/schedules/"+created.ID, nil, &read, http.StatusOK)
	if read.Title != input.Title || read.Prompt != input.Prompt || read.SessionID != p.Session.ID {
		t.Fatal("reading the plan changed its editable data")
	}
	var bootstrap Bootstrap
	scheduleOwnerCall(t, p, server, "GET", "/api/v1/bootstrap", nil, &bootstrap, http.StatusOK)
	if len(bootstrap.Schedules) != 1 || bootstrap.Schedules[0].ID != created.ID {
		t.Fatal("bootstrap omitted current schedules needed by the sidebar")
	}
	paused := input
	paused.Enabled, paused.Title, paused.Prompt = false, "Edited reminder", "Check deployment and inspect the logs."
	scheduleOwnerCall(t, p, server, "PATCH", "/api/v1/schedules/"+created.ID, ScheduleUpdate{Revision: created.Revision, Input: paused}, &read, http.StatusOK)
	if read.Enabled || read.NextRunAt != nil || read.Revision <= created.Revision || read.Prompt != paused.Prompt {
		t.Fatalf("Owner pause/edit did not take effect: %+v", read)
	}
	scheduleOwnerCall(t, p, server, "PATCH", "/api/v1/schedules/"+created.ID, ScheduleUpdate{Revision: created.Revision, Input: input}, nil, http.StatusConflict)
	scheduleOwnerCall(t, p, server, "DELETE", "/api/v1/schedules/"+created.ID+"?revision="+strconv.FormatInt(created.Revision, 10), nil, nil, http.StatusConflict)
	paused.Enabled = true
	scheduleOwnerCall(t, p, server, "PATCH", "/api/v1/schedules/"+created.ID, ScheduleUpdate{Revision: read.Revision, Input: paused}, &read, http.StatusOK)
	if !read.Enabled || read.NextRunAt == nil || !read.NextRunAt.Equal(at) {
		t.Fatal("resuming the one-time plan lost its future time")
	}
	scheduleOwnerCall(t, p, server, "DELETE", "/api/v1/schedules/"+created.ID+"?revision="+strconv.FormatInt(read.Revision, 10), nil, nil, http.StatusOK)
	scheduleOwnerCall(t, p, server, "GET", "/api/v1/schedules/"+created.ID, nil, nil, http.StatusNotFound)
	var plans []domain.Schedule
	scheduleOwnerCall(t, p, server, "GET", "/api/v1/schedules", nil, &plans, http.StatusOK)
	if len(plans) != 0 || p.Requests.Load() != 0 {
		t.Fatal("plan deletion retained a plan or managing schedules invoked a model")
	}
}

func TestOwnerNewSessionScheduleDoesNotRequireExistingTarget(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	input := map[string]any{
		"agent_id": p.Agent.ID, "session_mode": "new", "title": "Start a fresh conversation", "prompt": "Review the project independently.", "enabled": true,
		"timing": map[string]any{"kind": "once", "at": at.Format(time.RFC3339)},
	}
	var plan domain.Schedule
	scheduleOwnerCall(t, p, server, "POST", "/api/v1/schedules", input, &plan, http.StatusOK)
	assertMode := func(plan domain.Schedule, expected, sessionID string) {
		t.Helper()
		var fields map[string]any
		if err := json.Unmarshal(testkit.JSON(plan), &fields); err != nil {
			t.Fatal(err)
		}
		if fields["session_mode"] != expected || plan.SessionID != sessionID {
			t.Fatalf("unexpected target mode: mode=%v session=%q", fields["session_mode"], plan.SessionID)
		}
	}
	assertMode(plan, "new", "")
	if plan.AgentID != p.Agent.ID || plan.NextRunAt == nil || !plan.NextRunAt.Equal(at) {
		t.Fatal("new-Session mode changed the selected Bot or scheduled time")
	}
	var bootstrap Bootstrap
	scheduleOwnerCall(t, p, server, "GET", "/api/v1/bootstrap", nil, &bootstrap, http.StatusOK)
	if len(bootstrap.Schedules) != 1 || bootstrap.Schedules[0].ID != plan.ID || len(bootstrap.Sessions) != 1 {
		t.Fatal("new-Session plan was hidden or created its execution Session prematurely")
	}
	assertMode(bootstrap.Schedules[0], "new", "")
	// A target-mode edit replaces its editable fields as one configuration;
	// switching back to new must remove the previous session_id.
	delete(input, "agent_id")
	input["revision"], input["session_mode"], input["session_id"] = plan.Revision, "existing", p.Session.ID
	scheduleOwnerCall(t, p, server, "PATCH", "/api/v1/schedules/"+plan.ID, input, &plan, http.StatusOK)
	assertMode(plan, "existing", p.Session.ID)
	input["revision"], input["session_mode"] = plan.Revision, "new"
	delete(input, "session_id")
	scheduleOwnerCall(t, p, server, "PATCH", "/api/v1/schedules/"+plan.ID, input, &plan, http.StatusOK)
	assertMode(plan, "new", "")
	input["revision"], input["session_id"] = plan.Revision, p.Session.ID
	scheduleOwnerCall(t, p, server, "PATCH", "/api/v1/schedules/"+plan.ID, input, nil, http.StatusBadRequest)
	var unchanged domain.Schedule
	scheduleOwnerCall(t, p, server, "GET", "/api/v1/schedules/"+plan.ID, nil, &unchanged, http.StatusOK)
	assertMode(unchanged, "new", "")
	if unchanged.Revision != plan.Revision || p.Requests.Load() != 0 {
		t.Fatal("rejected target-mode edit changed the plan or ran a model")
	}
}

func TestOwnerScheduleAuthenticationAndTargetValidation(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	for _, endpoint := range []struct{ method, path string }{
		{"GET", "/api/v1/schedules"}, {"POST", "/api/v1/schedules"}, {"GET", "/api/v1/schedules/missing"},
		{"PATCH", "/api/v1/schedules/missing"}, {"DELETE", "/api/v1/schedules/missing?revision=1"}, {"GET", "/api/v1/schedules/events"},
	} {
		response := request(t, p.Context, server, endpoint.method, endpoint.path, nil, false)
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous schedule route accepted: %s %s -> %d", endpoint.method, endpoint.path, response.StatusCode)
		}
	}
	valid := schedule.Input{SessionID: p.Session.ID, Title: "Recurring plan", Prompt: "Review the project.", Enabled: true, Timing: domain.ScheduleTiming{Kind: "cron", Cron: "0 9 * * 1-5"}}
	for _, timing := range []domain.ScheduleTiming{
		{Kind: "cron", Cron: "@every 1h"}, {Kind: "cron", Cron: "invalid cron"},
		{Kind: "once"}, {Kind: "unsupported"},
	} {
		input := valid
		input.Timing = timing
		scheduleOwnerCall(t, p, server, "POST", "/api/v1/schedules", ScheduleCreate{AgentID: p.Agent.ID, Input: input}, nil, http.StatusBadRequest)
	}
	other := p.Agent
	other.Meta = domain.Meta{}
	if err := p.Harness.SaveAgent(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	foreign, err := p.Harness.CreateSession(p.Context, other.ID, "Other Bot's Session")
	if err != nil {
		t.Fatal(err)
	}
	wrongTarget := valid
	wrongTarget.SessionID = foreign.ID
	scheduleOwnerCall(t, p, server, "POST", "/api/v1/schedules", ScheduleCreate{AgentID: p.Agent.ID, Input: wrongTarget}, nil, http.StatusNotFound)
	var mine, theirs domain.Schedule
	scheduleOwnerCall(t, p, server, "POST", "/api/v1/schedules", ScheduleCreate{AgentID: p.Agent.ID, Input: valid}, &mine, http.StatusOK)
	scheduleOwnerCall(t, p, server, "POST", "/api/v1/schedules", ScheduleCreate{AgentID: other.ID, Input: wrongTarget}, &theirs, http.StatusOK)
	var all, filtered []domain.Schedule
	scheduleOwnerCall(t, p, server, "GET", "/api/v1/schedules", nil, &all, http.StatusOK)
	scheduleOwnerCall(t, p, server, "GET", "/api/v1/schedules?agent_id="+p.Agent.ID, nil, &filtered, http.StatusOK)
	if len(all) != 2 || len(filtered) != 1 || filtered[0].ID != mine.ID {
		t.Fatal("Owner all-Bot and selected-Bot lists did not preserve their scopes")
	}
	scheduleOwnerCall(t, p, server, "PATCH", "/api/v1/schedules/"+mine.ID, ScheduleUpdate{Revision: mine.Revision, Input: wrongTarget}, nil, http.StatusNotFound)
	var unchanged domain.Schedule
	scheduleOwnerCall(t, p, server, "GET", "/api/v1/schedules/"+mine.ID, nil, &unchanged, http.StatusOK)
	if unchanged.SessionID != p.Session.ID || unchanged.Revision != mine.Revision {
		t.Fatal("invalid cross-Bot retargeting changed the original plan")
	}
}

func TestOwnerBoundIMSessionRejectsRenameAndArchiveWithoutChangingPin(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	target := p.Session
	target.BindingID = "current-chat-binding"
	target.Title = "Platform chat title"
	if err := p.Store.Save(p.Context, "sessions", &target.Meta, &target); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/sessions/" + target.ID
	for _, input := range []any{
		map[string]any{"title": "Renamed"}, map[string]any{"archived": true}, map[string]any{"archived": false},
		map[string]any{"title": "Renamed", "pinned": true}, map[string]any{"archived": true, "pinned": true},
	} {
		scheduleOwnerCall(t, p, server, "PATCH", path, input, nil, http.StatusConflict)
	}
	if err := p.Store.Get(p.Context, "sessions", target.ID, &target); err != nil || target.Title != "Platform chat title" || target.Archived || target.Pinned {
		t.Fatalf("rejected combined edit partially changed the IM Session: %+v %v", target, err)
	}
	// Pinning remains an Owner presentation operation, independent of the IM
	// title/archive restrictions and absent from the Agent's Gateway schema.
	scheduleOwnerCall(t, p, server, "PATCH", path, map[string]any{"pinned": true}, nil, http.StatusOK)
	if err := p.Store.Get(p.Context, "sessions", target.ID, &target); err != nil || !target.Pinned || target.Title != "Platform chat title" {
		t.Fatalf("Owner could not pin the IM Session: %+v %v", target, err)
	}
}

package gateway_test

import (
	"net/http"
	"testing"

	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/recall"
)

func TestOwnerControlsPrivacyAndCommandScopeCannotBypassIt(t *testing.T) {
	f := newScheduleGateway(t)
	target, err := f.p.Harness.CreateSession(f.p.Context, f.p.Agent.ID, "Owner's private work")
	if err != nil {
		t.Fatal(err)
	}
	f.call(t, true, "PATCH", "/api/v1/sessions/"+target.ID, map[string]any{"private": true}, nil, http.StatusOK)
	var owner harness.View
	f.call(t, true, "GET", "/api/v1/sessions/"+target.ID, nil, &owner, http.StatusOK)
	if !owner.Private {
		t.Fatal("Owner privacy update was not persisted")
	}
	var catalog recall.SessionPage
	f.call(t, false, "GET", "/v1/sessions", nil, &catalog, http.StatusOK)
	for _, item := range catalog.Items {
		if item.ID == target.ID {
			t.Fatal("command credential could discover another private Session")
		}
	}
	f.call(t, false, "GET", "/v1/sessions/"+target.ID+"/history", nil, nil, http.StatusNotFound)
	f.call(t, false, "PATCH", "/v1/sessions/"+target.ID, map[string]any{"title": "stolen"}, nil, http.StatusNotFound)
	f.call(t, false, "POST", "/v1/schedules", scheduleInput(target.ID, "hidden target"), nil, http.StatusNotFound)
	f.call(t, false, "PATCH", "/v1/sessions/"+target.ID, map[string]any{"private": false}, nil, http.StatusBadRequest)
	// Setting the calling Session private cannot lock the Owner or its own
	// already-issued command identity out of that Session.
	f.call(t, true, "PATCH", "/api/v1/sessions/"+f.p.Session.ID, map[string]any{"private": true}, nil, http.StatusOK)
	var own gateway.Context
	f.call(t, false, "GET", "/v1/context", nil, &own, http.StatusOK)
	if !own.Session.Private || own.Session.ID != f.p.Session.ID {
		t.Fatal("private calling Session lost its own context")
	}
	f.call(t, false, "GET", "/v1/sessions/"+f.p.Session.ID+"/history", nil, nil, http.StatusOK)
	f.call(t, true, "PATCH", "/api/v1/sessions/"+target.ID, map[string]any{"private": false}, nil, http.StatusOK)
	f.call(t, false, "GET", "/v1/sessions/"+target.ID+"/history", nil, nil, http.StatusOK)
}

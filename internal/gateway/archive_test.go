package gateway_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
	"github.com/oines/astral/internal/wake"
)

func TestArchivedSourceCommandCannotChangeAnotherTargetsEndpoint(t *testing.T) {
	for _, kind := range []string{"root", "child", "legacy-ancestor"} {
		t.Run(kind, func(t *testing.T) {
			f := newScheduleGateway(t)
			p := f.p
			other, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "Keep another target active")
			if err != nil {
				t.Fatal(err)
			}
			var endpoint domain.WakeEndpoint
			f.call(t, false, http.MethodPost, "/v1/wake-endpoints", wake.CreateInput{Title: "Other target", SessionID: other.ID}, &endpoint, http.StatusCreated)
			source, archived := p.Session, p.Session
			if kind != "root" {
				source, err = p.Harness.CreateSession(p.Context, p.Agent.ID, "Existing delegated command")
				if err != nil {
					t.Fatal(err)
				}
				source.SubagentParentID = p.Session.ID
				if err = p.Store.Save(p.Context, "sessions", &source.Meta, &source); err != nil {
					t.Fatal(err)
				}
				if kind == "child" {
					archived = source
				}
			}
			binding, err := p.Hub.Bind(p.Context, p.Agent.ID, source.ID, p.Agent.DefaultComputerID, "")
			if err != nil {
				t.Fatal(err)
			}
			client := testkit.ComputerAPI(t, p, binding)
			// Keep the original command credential alive to exercise the
			// Gateway boundary before process cancellation has completed.
			archived.Archived = true
			if err = p.Store.Save(p.Context, "sessions", &archived.Meta, &archived); err != nil {
				t.Fatal(err)
			}
			notes := "forbidden change from an archived command"
			request, err := http.NewRequestWithContext(p.Context, http.MethodPatch, binding.APIURL+"/v1/wake-endpoints/"+endpoint.ID, bytes.NewReader(testkit.JSON(wake.Patch{Revision: endpoint.Revision, Notes: &notes})))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusConflict {
				t.Fatalf("archived %s retained its command API: status=%d body=%s", kind, response.StatusCode, raw)
			}
			var read domain.WakeEndpoint
			f.call(t, true, http.MethodGet, "/api/v1/wake-endpoints/"+endpoint.ID, nil, &read, http.StatusOK)
			if read.Notes != "" || !read.Enabled {
				t.Fatalf("the unrelated endpoint was changed: %+v", read)
			}
			notes = "Owner management remains available"
			f.call(t, true, http.MethodPatch, "/api/v1/wake-endpoints/"+endpoint.ID, wake.Patch{Revision: read.Revision, Notes: &notes}, &read, http.StatusOK)
		})
	}
}

func TestArchivedTargetEndpointsRemainOwnerAndOtherSessionManageable(t *testing.T) {
	f := newScheduleGateway(t)
	p := f.p
	other, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "Archive this delivery target")
	if err != nil {
		t.Fatal(err)
	}
	var endpoint domain.WakeEndpoint
	f.call(t, false, http.MethodPost, "/v1/wake-endpoints", wake.CreateInput{Title: "Archived target", SessionID: other.ID}, &endpoint, http.StatusCreated)
	archived := true
	if err = p.Harness.SaveSessionPresentation(p.Context, other.ID, nil, nil, &archived, nil); err != nil {
		t.Fatal(err)
	}
	f.call(t, true, http.MethodGet, "/api/v1/wake-endpoints/"+endpoint.ID, nil, &endpoint, http.StatusOK)
	if endpoint.Enabled {
		t.Fatal("archive did not persistently pause the endpoint")
	}
	notes, enabled := "retained script location", true
	f.call(t, false, http.MethodPatch, "/v1/wake-endpoints/"+endpoint.ID, wake.Patch{Revision: endpoint.Revision, Notes: &notes}, &endpoint, http.StatusOK)
	f.call(t, true, http.MethodPatch, "/api/v1/wake-endpoints/"+endpoint.ID, wake.Patch{Revision: endpoint.Revision, Enabled: &enabled}, nil, http.StatusConflict)
	f.call(t, false, http.MethodPost, "/v1/wake-endpoints", wake.CreateInput{Title: "Must be rejected", SessionID: other.ID}, nil, http.StatusConflict)
	response, err := http.Post(endpoint.TriggerURL, "application/json", bytes.NewReader(testkit.JSON(wake.Event{EventID: "archived", Text: "Do not queue"})))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("archived target accepted a hook: %d", response.StatusCode)
	}
}

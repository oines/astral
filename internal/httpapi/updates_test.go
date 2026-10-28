package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/oines/astral/internal/serverupdate"
	"github.com/oines/astral/internal/testkit"
)

type updateFixture struct{ checks, applies int }

func (u *updateFixture) State(context.Context) (serverupdate.State, error) {
	return serverupdate.State{Phase: "available", Manager: "systemd-user", CurrentVersion: "0.1.1", Version: "0.1.2"}, nil
}
func (u *updateFixture) Check(ctx context.Context) (serverupdate.State, error) {
	u.checks++
	return u.State(ctx)
}
func (u *updateFixture) Apply(context.Context) (serverupdate.State, error) {
	u.applies++
	return serverupdate.State{Phase: "checking", Manager: "systemd-user", CurrentVersion: "0.1.1", OperationID: "operation-1", OperationState: "running"}, nil
}

func TestServerUpdatesRequireOwnerAndAcceptOnlyAnEmptyIntent(t *testing.T) {
	p := testkit.New(t, 1, nil)
	api, server := apiFixture(t, p)
	controller := &updateFixture{}
	api.Config.Updates = controller
	for _, path := range []string{"/updates", "/updates/check", "/updates/apply"} {
		method := "POST"
		if path == "/updates" {
			method = "GET"
		}
		response := request(t, p.Context, server, method, "/api/v1"+path, nil, false)
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous update accepted: %d", response.StatusCode)
		}
	}
	for _, path := range []string{"/updates/check", "/updates/apply"} {
		for _, body := range []string{`{"url":"https://attacker.invalid/update"}`, `{"root":"/other"}`, `{"version":"99.0.0"}`, `{} {}`} {
			response := request(t, p.Context, server, "POST", "/api/v1"+path, strings.NewReader(body), true)
			response.Body.Close()
			if response.StatusCode != 400 {
				t.Fatalf("accepted an arbitrary update parameter: %s %d", path, response.StatusCode)
			}
		}
	}
	if controller.checks != 0 || controller.applies != 0 {
		t.Fatal("rejected input reached the update controller")
	}
	response := request(t, p.Context, server, "POST", "/api/v1/updates/check", nil, true)
	response.Body.Close()
	if response.StatusCode != 200 || controller.checks != 1 || controller.applies != 0 {
		t.Fatal("metadata check started an installation")
	}
	response = request(t, p.Context, server, "POST", "/api/v1/updates/apply", strings.NewReader(`{}`), true)
	defer response.Body.Close()
	var state serverupdate.State
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || controller.applies != 1 || state.OperationID != "operation-1" || state.OperationState != "running" {
		t.Fatal("update did not report the independently managed operation")
	}
}

func TestUnmanagedServerCannotRequestReplacement(t *testing.T) {
	p := testkit.New(t, 1, nil)
	_, server := apiFixture(t, p)
	response := request(t, p.Context, server, "GET", "/api/v1/updates", nil, true)
	var state serverupdate.State
	err := json.NewDecoder(response.Body).Decode(&state)
	response.Body.Close()
	if err != nil || state.Phase != "unavailable" || state.Manager != "manual" {
		t.Fatalf("unmanaged installation advertised replacement: %+v %v", state, err)
	}
	response = request(t, p.Context, server, "POST", "/api/v1/updates/apply", nil, true)
	response.Body.Close()
	if response.StatusCode != 409 {
		t.Fatal("unmanaged Server update was not refused")
	}
}

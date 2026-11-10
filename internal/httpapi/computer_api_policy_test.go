package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/testkit"
)

func TestComputerAPIPolicyAcrossComputers(t *testing.T) {
	p := testkit.New(t, 2, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	p.Hub.Gateway = gateway.New(p.Harness, channels.New(p.Store, p.Harness)).Handle

	owner := func(method, path, payload string, want int, output any) {
		t.Helper()
		response := request(t, p.Context, server, method, "/api/v1"+path, strings.NewReader(payload), true)
		defer response.Body.Close()
		if response.StatusCode != want {
			body, _ := io.ReadAll(response.Body)
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, response.StatusCode, want, body)
		}
		if output != nil {
			if err := json.NewDecoder(response.Body).Decode(output); err != nil {
				t.Fatal(err)
			}
		}
	}

	for _, tc := range []struct {
		body     string
		disabled bool
	}{
		{`{"container":false,"server_url":"https://astral.example"}`, true},
		{`{"container":true,"server_url":"https://astral.example","computer_api_disabled":false}`, false},
	} {
		var pending computers.EnrollmentResult
		owner("POST", "/computers/enrollments", tc.body, 200, &pending)
		credentials, err := p.Hub.Register(p.Context, pending.Enrollment.ID, computers.EnrollmentRequest{
			Token: pending.Token, Hello: p.Daemons[0].Hello,
		})
		if err != nil {
			t.Fatal(err)
		}
		var registered domain.Computer
		if err = p.Store.Get(p.Context, "computers", credentials.ComputerID, &registered); err != nil {
			t.Fatal(err)
		}
		if pending.Enrollment.ComputerAPIDisabled != tc.disabled || registered.ComputerAPIDisabled != tc.disabled {
			t.Fatal("registration did not inherit the Server's pairing policy")
		}
	}

	var a domain.Computer
	if err := p.Store.Get(p.Context, "computers", p.Daemons[0].Credentials.ComputerID, &a); err != nil {
		t.Fatal(err)
	}
	bind := func(id string) domain.ExecutionBinding {
		t.Helper()
		binding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, id, "")
		if err != nil {
			t.Fatal(err)
		}
		return binding
	}
	source := bind(a.ID)
	target := bind(p.Daemons[1].Credentials.ComputerID)
	file := filepath.Join(target.AgentHome, "target.txt")
	if err := os.WriteFile(file, []byte("target content"), 0600); err != nil {
		t.Fatal(err)
	}
	read := func(client *http.Client, from, to domain.ExecutionBinding, want int, contains string) {
		t.Helper()
		endpoint := from.APIURL + "/v1/computers/" + to.ComputerID + "/files/content?" + url.Values{"path": {file}}.Encode()
		req, err := http.NewRequestWithContext(p.Context, "GET", endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want || !strings.Contains(string(body), contains) {
			t.Fatalf("cross-Computer request: got HTTP %d, want %d: %s (%v)", response.StatusCode, want, body, err)
		}
	}

	before := testkit.ComputerAPI(t, p, source)
	trusted := testkit.ComputerAPI(t, p, target)
	read(before, source, target, 200, "target content")
	set := func(disabled bool) {
		t.Helper()
		var updated domain.Computer
		owner("PATCH", "/computers/"+a.ID, string(testkit.JSON(map[string]any{
			"revision": a.Revision, "computer_api_disabled": disabled,
		})), 200, &updated)
		a = updated
	}
	set(true)
	read(before, source, target, 403, "computer_api_disabled")
	during := testkit.ComputerAPI(t, p, source)
	read(during, source, target, 403, "computer_api_disabled")
	read(trusted, target, source, 200, "target content")

	owner("PATCH", "/computers/"+a.ID, string(testkit.JSON(map[string]any{
		"revision": a.Revision, "name": "Renamed while disabled",
	})), 200, &a)
	read(during, source, target, 403, "computer_api_disabled")
	set(false)
	read(before, source, target, 401, "unauthorized")
	read(during, source, target, 401, "unauthorized")
	read(testkit.ComputerAPI(t, p, source), source, target, 200, "target content")
}

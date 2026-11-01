package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestContainerHostCheckIsAuthenticatedFreshAndUsesAnExplicitProbeBudget(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	api, server := apiFixture(t, p)
	calls := 0
	api.Config.HostInfo = func(ctx context.Context) domain.HostInfo {
		calls++
		if calls < 3 {
			if deadline, set := ctx.Deadline(); set && time.Until(deadline) < 10*time.Second {
				t.Errorf("explicit Docker check received the short bootstrap deadline")
			}
		} else if deadline, set := ctx.Deadline(); !set || time.Until(deadline) > 4*time.Second {
			t.Errorf("page bootstrap did not bound its optional Docker check")
		}
		if calls == 1 {
			return domain.HostInfo{ContainerStatus: "permission_denied", ContainerUser: "server-owner", ContainerError: "original socket permission error"}
		}
		return domain.HostInfo{ContainerAvailable: true, ContainerStatus: "available", ContainerUser: "server-owner", ContainerRuntime: "existing Docker"}
	}
	anonymous := request(t, p.Context, server, "GET", "/api/v1/computers/host", nil, false)
	anonymous.Body.Close()
	if anonymous.StatusCode != 401 || calls != 0 {
		t.Fatal("anonymous request reached the Docker check")
	}
	for index := range 2 {
		response := request(t, p.Context, server, "GET", "/api/v1/computers/host", nil, true)
		var host domain.HostInfo
		err := json.NewDecoder(response.Body).Decode(&host)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || (index == 0 && host.ContainerStatus != "permission_denied") || (index == 1 && !host.ContainerAvailable) {
			t.Fatalf("Docker check did not return the current Server-side result: %+v %v", host, err)
		}
	}
	bootstrap := request(t, p.Context, server, "GET", "/api/v1/bootstrap", nil, true)
	bootstrap.Body.Close()
	if bootstrap.StatusCode != 200 || calls != 3 {
		t.Fatalf("unexpected probe count or bootstrap result: %d %d", calls, bootstrap.StatusCode)
	}
}

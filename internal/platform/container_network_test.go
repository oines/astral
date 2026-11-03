package platform

import (
	"context"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestContainerNetworkPreservesRemoteServer(t *testing.T) {
	for _, address := range []string{"https://server.example.com", "http://100.64.0.1:4545", "http://192.168.1.10:4545"} {
		network, got, err := containerRoute(context.Background(), "unused-image", "amd64", "unused-program", ConnectOptions{ServerURL: address}, nil, nil)
		if err != nil || network != "bridge" || got != address {
			t.Fatalf("remote Server changed: %q %q %v", network, got, err)
		}
	}
}

func TestLocalContainerCandidatesUseOnlyHostAliasesAndListenerPort(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:41234", "http://[::1]:49876", "https://localhost:4545"} {
		base, err := localContainerBase(raw)
		if err != nil {
			t.Fatal(err)
		}
		urls := containerHostAliasURLs(base)
		if len(urls) != len(containerHostAliases) {
			t.Fatalf("unexpected local routes: %v", urls)
		}
		for i, candidate := range urls {
			address, err := url.Parse(candidate)
			if err != nil || address.Hostname() != containerHostAliases[i] || address.Scheme != base.Scheme || address.Port() != base.Port() || !isLocalContainerAddress(candidate, raw) {
				t.Fatalf("local route escaped its listener: %s from %s", candidate, raw)
			}
		}
		for _, remote := range []string{"https://public.example.com", "http://192.168.1.10:4545", "http://100.64.0.1:4545"} {
			if isLocalContainerAddress(remote, raw) {
				t.Fatalf("remote route admitted as local: %s", remote)
			}
		}
	}
	for _, raw := range []string{"https://public.example.com", "http://192.168.1.10:4545", "http://127.0.0.1:4545/prefix", "http://user:password@127.0.0.1:4545"} {
		if _, err := localContainerBase(raw); err == nil {
			t.Fatalf("strict local URL accepted: %s", raw)
		}
	}
}

func TestLocalContainerRouteUsesTheSuccessfulProbeNetwork(t *testing.T) {
	for _, test := range []struct {
		plan, network string
		gateway       bool
	}{
		{"native_bridge", "bridge", false},
		{"mapped_bridge", "bridge", true},
		{"host", "host", false},
	} {
		t.Run(test.plan, func(t *testing.T) {
			server, ctx, statePath, read := provisioningFixture(t)
			ctx = dockerContext(ctx, DockerInfo{Endpoint: "unix:///astral-provision-test.sock"})
			state := read()
			state.ProbeResult = test.plan
			// A runtime with native aliases need not support host-gateway at all.
			state.RejectGateway = test.plan == "native_bridge"
			if err := WriteJSON(statePath, state); err != nil {
				t.Fatal(err)
			}
			image, err := computerImageReference()
			if err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(server.Config.DistributionDirectory, "astral-linux-amd64")
			route, err := localContainerRoute(ctx, image, "amd64", binary, server.URL, server.Hub.ServerPin(), nil)
			if err != nil || route.network != test.network || route.addHostGateway != test.gateway || !isLocalContainerAddress(route.serverURL, server.URL) {
				t.Fatalf("wrong local route: %+v error=%v", route, err)
			}
			state = read()
			if state.ProbePin != server.Hub.ServerPin() {
				t.Fatal("probe did not retain the Server identity")
			}
			for _, command := range state.Commands {
				if command[0] != "create" {
					continue
				}
				if !strings.Contains(strings.Join(command, " "), image) || !strings.Contains(strings.Join(command, " "), "linux/amd64") {
					t.Fatalf("probe changed image or architecture: %v", command)
				}
				if test.plan == "native_bridge" && (strings.Contains(strings.Join(command, " "), "--add-host") || strings.Contains(strings.Join(command, " "), "--network host")) {
					t.Fatalf("native bridge unnecessarily required host mapping/network: %v", command)
				}
			}
		})
	}
}

func TestLocalContainerLaunchIgnoresLegacyPublicEnrollmentAndAlternatives(t *testing.T) {
	for _, plan := range []string{"native_bridge", "mapped_bridge", "host"} {
		t.Run(plan, func(t *testing.T) {
			server, ctx, statePath, read := provisioningFixture(t)
			state := read()
			state.ProbeResult = plan
			state.RejectGateway = plan == "native_bridge"
			if err := WriteJSON(statePath, state); err != nil {
				t.Fatal(err)
			}
			enrollmentPath := filepath.Join(filepath.Dir(statePath), "legacy-enrollment.json")
			enrollment := ConnectOptions{ServerURL: "https://legacy-public.example.com", ServerPin: server.Hub.ServerPin(), Token: "not-a-probe-credential"}
			if err := WriteJSON(enrollmentPath, enrollment); err != nil {
				t.Fatal(err)
			}
			options := DockerOptions{Identity: "strict-local", Binary: filepath.Join(server.Config.DistributionDirectory, "astral-linux-amd64"), EnrollmentFile: enrollmentPath,
				LocalServerURL: server.URL, ServerURLs: []string{"https://configured-public.example.com", "http://192.168.1.99:4545"}}
			if _, err := LaunchContainer(ctx, options, nil); err != nil {
				t.Fatal(err)
			}
			var selected ConnectOptions
			if err := ReadJSON(enrollmentPath, &selected); err != nil || !isLocalContainerAddress(selected.ServerURL, server.URL) || selected.ServerPin != enrollment.ServerPin || selected.Token != enrollment.Token {
				t.Fatalf("local selection changed identity or retained public route: %+v %v", selected, err)
			}
			for _, command := range read().Commands {
				joined := strings.Join(command, " ")
				if strings.Contains(joined, "example.com") || strings.Contains(joined, "192.168.1.99") || strings.Contains(joined, enrollment.Token) {
					t.Fatalf("local Docker/probe operation received a public route or enrollment credential: %v", command)
				}
				if command[0] == "create" && !strings.Contains(joined, "astral.temporary=network-check") {
					wantNetwork := "bridge"
					if plan == "host" {
						wantNetwork = "host"
					}
					if !strings.Contains(joined, "--network "+wantNetwork) || strings.Contains(joined, "--add-host") != (plan == "mapped_bridge") {
						t.Fatalf("final container differs from the successful probe plan %s: %v", plan, command)
					}
				}
			}
		})
	}
}

func TestLocalContainerRouteFailureNeverFallsBackToPublicEnrollment(t *testing.T) {
	server, ctx, statePath, read := provisioningFixture(t)
	state := read()
	state.ProbeResult = "none"
	if err := WriteJSON(statePath, state); err != nil {
		t.Fatal(err)
	}
	enrollmentPath := filepath.Join(filepath.Dir(statePath), "legacy-enrollment.json")
	enrollment := ConnectOptions{ServerURL: "https://legacy-public.example.com", ServerPin: server.Hub.ServerPin()}
	if err := WriteJSON(enrollmentPath, enrollment); err != nil {
		t.Fatal(err)
	}
	options := DockerOptions{Identity: "strict-local-failed", Binary: filepath.Join(server.Config.DistributionDirectory, "astral-linux-amd64"), EnrollmentFile: enrollmentPath,
		LocalServerURL: server.URL, ServerURLs: []string{"https://configured-public.example.com", "http://192.168.1.99:4545"}}
	if _, err := LaunchContainer(ctx, options, nil); err == nil || !strings.Contains(err.Error(), "on this machine") {
		t.Fatalf("failed local route did not explain local access: %v", err)
	}
	state = read()
	if state.ContainerID != "" || state.Volume != "" || state.Starts != 0 {
		t.Fatal("failed local route created or started a working container")
	}
	for _, command := range state.Commands {
		if strings.Contains(strings.Join(command, " "), "example.com") || strings.Contains(strings.Join(command, " "), "192.168.1.99") {
			t.Fatalf("local route attempted a remote fallback: %v", command)
		}
	}
	var after ConnectOptions
	if err := ReadJSON(enrollmentPath, &after); err != nil || !reflect.DeepEqual(after, enrollment) {
		t.Fatalf("failed route rewrote the enrollment: %+v %v", after, err)
	}
}

func TestContainerHostAliasesWorkForManualUpdates(t *testing.T) {
	for _, alias := range containerHostAliases {
		address := "http://" + alias + ":4545/prefix"
		if got := containerServerURLOnHost(address); got != "http://127.0.0.1:4545/prefix" {
			t.Fatalf("host cannot query the container's route %q: %q", address, got)
		}
	}
	for _, address := range []string{"http://192.168.1.10:4545", "https://server.example.com", "http://100.64.0.1:4545"} {
		if got := containerServerURLOnHost(address); got != address {
			t.Fatalf("remote route was rewritten: %s", got)
		}
	}
}

func TestContainerHostCandidatesExcludeLoopbackAndDuplicates(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:4545")
	got := containerHostURLs(base, []string{"http://100.64.0.1:4545", "http://100.64.0.1:4545/", "http://127.0.0.1:4545", "https://user:password@example.com"})
	seen := map[string]bool{}
	for _, candidate := range got {
		address, err := url.Parse(candidate)
		if err != nil || isLoopbackHost(address.Hostname()) || seen[candidate] || address.User != nil {
			t.Fatalf("invalid or duplicate route: %q", candidate)
		}
		seen[candidate] = true
	}
	for _, alias := range containerHostAliases {
		if !seen["http://"+alias+":4545"] {
			t.Fatalf("missing runtime route %s: %s", alias, strings.Join(got, ", "))
		}
	}
}

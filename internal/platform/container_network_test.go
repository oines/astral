package platform

import (
	"context"
	"net/url"
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

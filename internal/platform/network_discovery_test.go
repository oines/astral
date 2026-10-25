package platform

import (
	"strings"
	"testing"
)

func TestNetworkTailscaleRequiresActualServeForHTTPS(t *testing.T) {
	status := []byte(`{"BackendState":"Running","Self":{"DNSName":"node.tailnet.ts.net.","TailscaleIPs":["100.64.1.2"],"Online":true}}`)
	reachable := func(string) bool { return true }
	plain := tailscaleAddresses(status, []byte(`{}`), "4545", "http://127.0.0.1:4545", reachable)
	if len(plain) != 2 || plain[0].URL != "http://node.tailnet.ts.net:4545" || strings.HasPrefix(plain[0].URL, "https:") {
		t.Fatalf("fabricated HTTPS without Serve: %+v", plain)
	}
	serve := []byte(`{"TCP":{"443":{"HTTPS":true}},"Web":{"node.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:4545"}}}}}`)
	active := tailscaleAddresses(status, serve, "4545", "http://127.0.0.1:4545", reachable)
	if len(active) != 3 || active[0].URL != "https://node.tailnet.ts.net" || active[0].Kind != "tailscale-serve" {
		t.Fatalf("actual Serve not preferred: %+v", active)
	}
	for _, invalid := range []string{
		`{"TCP":{"443":{"HTTPS":true}},"Web":{"node.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:9999"}}}}}`,
		`{"TCP":{"443":{"HTTP":true}},"Web":{"node.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:4545"}}}}}`,
		`{"TCP":{"443":{"HTTPS":true}},"Web":{"node.tailnet.ts.net:443":{"Handlers":{"/other":{"Proxy":"http://127.0.0.1:4545"}}}}}`,
	} {
		values := tailscaleAddresses(status, []byte(invalid), "4545", "http://127.0.0.1:4545", reachable)
		if len(values) != 2 || values[0].Kind == "tailscale-serve" {
			t.Fatalf("unrelated Serve advertised: %+v", values)
		}
	}
	restricted := tailscaleAddresses(status, []byte(`{}`), "4545", "http://127.0.0.1:4545", func(string) bool { return false })
	if len(restricted) != 0 {
		t.Fatalf("loopback-only bind advertised tailnet: %+v", restricted)
	}
}

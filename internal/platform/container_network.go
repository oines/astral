package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
)

var containerHostAliases = []string{"host.docker.internal", "host.orb.internal", "host.lima.internal", "host.rancher-desktop.internal", "astral.host.internal"}

type localContainerNetwork struct {
	network        string
	serverURL      string
	addHostGateway bool
}

func localContainerBase(raw string) (*url.URL, error) {
	base, err := url.Parse(raw)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || !isLoopbackHost(base.Hostname()) || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" {
		return nil, domain.Invalid("the local container requires this Server's loopback listening address")
	}
	base.Path = ""
	return base, nil
}

func containerHostAliasURLs(base *url.URL) []string {
	result := make([]string, 0, len(containerHostAliases))
	for _, host := range containerHostAliases {
		u := *base
		u.Host = host
		if port := base.Port(); port != "" {
			u.Host = net.JoinHostPort(host, port)
		}
		result = append(result, strings.TrimRight(u.String(), "/"))
	}
	return result
}

func isLocalContainerAddress(raw, localURL string) bool {
	base, err := localContainerBase(localURL)
	if err != nil {
		return false
	}
	address, err := url.Parse(raw)
	if err != nil || address.Scheme != base.Scheme || address.Port() != base.Port() || address.User != nil || address.RawQuery != "" || address.Fragment != "" || address.Path != "" && address.Path != "/" {
		return false
	}
	if isLoopbackHost(address.Hostname()) {
		return true
	}
	for _, host := range containerHostAliases {
		if strings.EqualFold(address.Hostname(), host) {
			return true
		}
	}
	return false
}

// Local provisioning must stay on this host even when an enrollment or the
// public Web settings contain a reachable reverse-proxy address.
func localContainerRoute(ctx context.Context, image, arch, binary, localURL, pin string, report func(string)) (localContainerNetwork, error) {
	base, err := localContainerBase(localURL)
	if err != nil {
		return localContainerNetwork{}, err
	}
	aliases := containerHostAliasURLs(base)
	plans := []struct {
		network        string
		addHostGateway bool
		urls           []string
		budget         time.Duration
	}{
		{"bridge", false, aliases, 20 * time.Second},
		{"bridge", true, aliases[len(aliases)-1:], 10 * time.Second},
		{"host", false, append([]string{base.String()}, aliases...), 20 * time.Second},
	}
	probeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for _, plan := range plans {
		// Each address probe already has a three-second CLI budget. Bound the
		// Docker operation too, so one stalled plan cannot consume every route.
		attemptCtx, stopAttempt := context.WithTimeout(probeCtx, plan.budget)
		selected, probeErr := probeContainerURLsWithGateway(attemptCtx, image, arch, plan.network, plan.addHostGateway, binary, pin, plan.urls)
		stopAttempt()
		if probeErr == nil {
			if report != nil {
				report("Container can reach the local Server at " + selected + ".")
			}
			return localContainerNetwork{network: plan.network, serverURL: selected, addHostGateway: plan.addHostGateway}, nil
		}
		if probeCtx.Err() != nil {
			return localContainerNetwork{}, fmt.Errorf("check container access to the local Server: %w", probeCtx.Err())
		}
	}
	return localContainerNetwork{}, domain.Invalid("the container cannot reach the Server on this machine; check the Server listening address, local firewall and container networking")
}

func containerServerURLOnHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	for _, alias := range containerHostAliases {
		if strings.EqualFold(u.Hostname(), alias) {
			port := u.Port()
			u.Host = "127.0.0.1"
			if port != "" {
				u.Host = net.JoinHostPort("127.0.0.1", port)
			}
			return u.String()
		}
	}
	return raw
}

// Docker's host network may belong to a VM or rootless namespace. Probe the
// route from an actual disposable container instead of inferring it from a
// provider name. Authentication still uses the original Server's pinned TLS.
func containerRoute(ctx context.Context, image, arch, binary string, enrollment ConnectOptions, alternatives []string, report func(string)) (string, string, error) {
	serverURL := enrollment.ServerURL
	base, err := url.Parse(serverURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return "", "", domain.Invalid("invalid Server address for the container")
	}
	// A remote LAN/Tailscale/reverse-proxy address already names the intended
	// machine. Do not replace it with addresses of this Docker host.
	if !isLoopbackHost(base.Hostname()) {
		return "bridge", serverURL, nil
	}
	candidates := containerHostURLs(base, alternatives)
	probeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for _, network := range []string{"bridge", "host"} {
		urls := candidates
		if network == "host" {
			urls = append([]string{serverURL}, candidates...)
		}
		selected, probeErr := probeContainerURLs(probeCtx, image, arch, network, binary, enrollment.ServerPin, urls)
		if probeErr == nil {
			if report != nil {
				report("Container can reach the Server at " + selected + ".")
			}
			return network, selected, nil
		}
		if probeCtx.Err() != nil {
			return "", "", fmt.Errorf("check container access to the Server: %w", probeCtx.Err())
		}
	}
	return "", "", domain.Invalid("the container cannot reach the Astral Server; check the Server listening address, local firewall and container networking, or use a reachable LAN/Tailscale access address")
}

func containerHostURLs(base *url.URL, alternatives []string) []string {
	var result []string
	add := func(raw string) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" || isLoopbackHost(u.Hostname()) {
			return
		}
		value := strings.TrimRight(u.String(), "/")
		for _, previous := range result {
			if previous == value {
				return
			}
		}
		result = append(result, value)
	}
	for _, candidate := range alternatives {
		add(candidate)
	}
	for _, candidate := range containerHostAliasURLs(base) {
		add(candidate)
	}
	port := base.Port()
	addresses, _ := net.InterfaceAddrs()
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() {
			continue
		}
		u := *base
		u.Host = ip.String()
		if port != "" {
			u.Host = net.JoinHostPort(ip.String(), port)
		}
		add(u.String())
	}
	return result
}

func probeContainerURLs(ctx context.Context, image, arch, network, binary, pin string, candidates []string) (string, error) {
	return probeContainerURLsWithGateway(ctx, image, arch, network, network == "bridge", binary, pin, candidates)
}

func probeContainerURLsWithGateway(ctx context.Context, image, arch, network string, addHostGateway bool, binary, pin string, candidates []string) (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	name := "astral-network-check-" + hex.EncodeToString(nonce[:])
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = dockerOutput(cleanup, "rm", "--force", name)
	}()
	args := []string{"create", "--pull", "never", "--name", name, "--platform", "linux/" + arch, "--network", network, "--label", "astral.temporary=network-check", "--entrypoint", "/bin/sh"}
	if addHostGateway {
		args = append(args, "--add-host", "astral.host.internal:host-gateway")
	}
	const script = `pin="$1"
shift
for address do
  if /usr/local/bin/astral computer probe --server "$address" --server-pin "$pin" >/dev/null 2>&1; then
    printf '%s\n' "$address"
    exit 0
  fi
done
exit 1`
	args = append(args, image, "-c", script, "astral-network-check")
	args = append(args, pin)
	args = append(args, candidates...)
	raw, err := dockerOutput(ctx, args...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if id == "" || strings.ContainsAny(id, " \r\n\t") {
		return "", fmt.Errorf("docker did not return a valid network-check container")
	}
	if err = injectContainerDaemon(ctx, id, binary); err != nil {
		return "", err
	}
	raw, err = dockerOutput(ctx, "start", "--attach", id)
	if err != nil {
		return "", err
	}
	selected := strings.TrimSpace(string(raw))
	for _, candidate := range candidates {
		if selected == candidate {
			return selected, nil
		}
	}
	return "", fmt.Errorf("container network check returned an unexpected address")
}

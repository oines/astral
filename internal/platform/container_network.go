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
	port := base.Port()
	for _, host := range containerHostAliases {
		u := *base
		u.Host = host
		if port != "" {
			u.Host = net.JoinHostPort(host, port)
		}
		add(u.String())
	}
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
	if network == "bridge" {
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

package platform

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oines/astral/internal/httpapi"
)

// These narrow JSON views follow Tailscale's public ipn ServeConfig and
// ipnstate.Status. Unknown fields are intentionally irrelevant to discovery.
// https://github.com/tailscale/tailscale/blob/main/ipn/serve.go
// https://tailscale.com/docs/reference/tailscale-cli/serve
// A MagicDNS name alone never implies an HTTPS endpoint.
type tailscaleStatus struct {
	BackendState string
	Self         *struct {
		DNSName      string
		TailscaleIPs []string
		Online       bool
	}
}
type tailscaleServe struct {
	TCP map[string]struct{ HTTPS bool }
	Web map[string]struct {
		Handlers map[string]struct{ Proxy string }
	}
	Foreground map[string]tailscaleServe
}

func discoverNetworkAddresses(ctx context.Context, listen, loopbackURL string) []httpapi.NetworkAddress {
	host, port, _ := net.SplitHostPort(listen)
	ip := net.ParseIP(host)
	wildcard := host == "" || ip != nil && ip.IsUnspecified()
	reachable := func(address string) bool { return wildcard || ip != nil && ip.Equal(net.ParseIP(address)) }
	var statusRaw, serveRaw []byte
	if command := tailscaleExecutable(); command != "" {
		commandCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			statusRaw = tailscaleOutput(commandCtx, command, "status", "--json")
		}()
		go func() {
			defer workers.Done()
			serveRaw = tailscaleOutput(commandCtx, command, "serve", "status", "--json")
		}()
		workers.Wait()
	}
	result := tailscaleAddresses(statusRaw, serveRaw, port, loopbackURL, reachable)
	interfaces, _ := net.Interfaces()
	var lan []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			value, _, err := net.ParseCIDR(address.String())
			if err != nil || value.To4() == nil || !value.IsGlobalUnicast() || !reachable(value.String()) || tailscaleIP(value) {
				continue
			}
			lan = append(lan, value.String())
		}
	}
	sort.Strings(lan)
	for _, address := range lan {
		result = append(result, httpapi.NetworkAddress{URL: "http://" + net.JoinHostPort(address, port), Label: address, Kind: "lan"})
	}
	result = append(result, httpapi.NetworkAddress{URL: loopbackURL, Label: "This computer", Kind: "loopback"})
	return result
}
func tailscaleIP(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}
func tailscaleAddresses(statusRaw, serveRaw []byte, port, loopbackURL string, reachable func(string) bool) []httpapi.NetworkAddress {
	var status tailscaleStatus
	if json.Unmarshal(statusRaw, &status) != nil || status.BackendState != "Running" || status.Self == nil || !status.Self.Online {
		return nil
	}
	var result []httpapi.NetworkAddress
	var serve tailscaleServe
	if json.Unmarshal(serveRaw, &serve) == nil {
		result = append(result, serveAddresses(serve, loopbackURL)...)
	}
	anyReachable := false
	for _, address := range status.Self.TailscaleIPs {
		if reachable(address) {
			anyReachable = true
		}
	}
	name := strings.TrimSuffix(status.Self.DNSName, ".")
	if anyReachable && strings.Contains(name, ".") {
		result = append(result, httpapi.NetworkAddress{URL: "http://" + net.JoinHostPort(name, port), Label: name, Kind: "tailscale-dns"})
	}
	for _, address := range status.Self.TailscaleIPs {
		if reachable(address) {
			result = append(result, httpapi.NetworkAddress{URL: "http://" + net.JoinHostPort(address, port), Label: address, Kind: "tailscale-ip"})
		}
	}
	return result
}
func serveAddresses(config tailscaleServe, loopbackURL string) []httpapi.NetworkAddress {
	var result []httpapi.NetworkAddress
	keys := make([]string, 0, len(config.Web))
	for key := range config.Web {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	local, _ := url.Parse(loopbackURL)
	for _, key := range keys {
		handler, ok := config.Web[key].Handlers["/"]
		if !ok {
			continue
		}
		host, port, err := net.SplitHostPort(key)
		if err != nil || !config.TCP[port].HTTPS {
			continue
		}
		target, err := url.Parse(handler.Proxy)
		if err != nil || target.Scheme != "http" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Path != "" && target.Path != "/" {
			continue
		}
		targetHost := target.Hostname()
		targetIP := net.ParseIP(targetHost)
		if targetHost != "localhost" && (targetIP == nil || !targetIP.IsLoopback()) {
			continue
		}
		// Serve must proxy this Server's actual loopback listener. A public-IP bind
		// has an auxiliary listener whose port can differ from the main listener.
		targetPort := target.Port()
		if targetPort == "" {
			targetPort = "80"
		}
		if local == nil || targetPort != local.Port() || targetHost != "localhost" && targetHost != local.Hostname() {
			continue
		}
		authority := net.JoinHostPort(host, port)
		if port == "443" {
			authority = host
		}
		result = append(result, httpapi.NetworkAddress{URL: "https://" + authority, Label: host + " (Tailscale Serve)", Kind: "tailscale-serve"})
	}
	foregroundKeys := make([]string, 0, len(config.Foreground))
	for key := range config.Foreground {
		foregroundKeys = append(foregroundKeys, key)
	}
	sort.Strings(foregroundKeys)
	for _, key := range foregroundKeys {
		result = append(result, serveAddresses(config.Foreground[key], loopbackURL)...)
	}
	return result
}

// Desktop processes launched by Dock or Explorer often inherit a minimal PATH.
// Probe installed executable locations without installing or starting a service.
func tailscaleExecutable() string {
	if command, err := exec.LookPath("tailscale"); err == nil {
		return command
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale", "/opt/homebrew/bin/tailscale", "/usr/local/bin/tailscale"}
	case "windows":
		programFiles := os.Getenv("ProgramFiles")
		if programFiles == "" {
			programFiles = `C:\Program Files`
		}
		candidates = []string{filepath.Join(programFiles, "Tailscale", "tailscale.exe")}
	default:
		candidates = []string{"/usr/bin/tailscale", "/usr/local/bin/tailscale"}
	}
	for _, candidate := range candidates {
		if command, err := exec.LookPath(candidate); err == nil {
			return command
		}
	}
	return ""
}
func tailscaleOutput(ctx context.Context, executable string, args ...string) []byte {
	command := exec.CommandContext(ctx, executable, args...)
	// The official macOS app also contains its CLI. This flag is essential when
	// Astral is launched outside a terminal: discovery must never open its GUI.
	// https://tailscale.com/docs/reference/tailscale-cli?tab=macos
	command.Env = append(os.Environ(), "TAILSCALE_BE_CLI=1")
	output, _ := command.Output()
	return output
}

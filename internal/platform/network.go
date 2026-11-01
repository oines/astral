package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpapi"
	"github.com/oines/astral/internal/serviceurl"
)

const defaultListen = "0.0.0.0:4545"

// NetworkValues contains only explicitly supplied values. In particular false
// and an absent boolean are different; environment and CLI callers preserve it.
type NetworkValues struct {
	Listen                *string   `json:"listen,omitempty"`
	PublicURLs            *[]string `json:"public_urls,omitempty"`
	AllowRemoteManagement *bool     `json:"allow_remote_management,omitempty"`
	RestrictHostnames     *bool     `json:"restrict_hostnames,omitempty"`
	AllowedHostnames      *[]string `json:"allowed_hostnames,omitempty"`
}

type networkState struct {
	discover                func(context.Context, string, string) []httpapi.NetworkAddress
	discoveryMu             sync.Mutex
	discoveredAt            time.Time
	mu                      sync.RWMutex
	path                    string
	cli, environment, saved NetworkValues
	activeListen            httpapi.NetworkStringSetting
	addresses               []httpapi.NetworkAddress
	desktop                 bool
}

func newNetworkState(config Config) (*networkState, error) {
	return loadNetworkState(config, true)
}

func loadNetworkState(config Config, persistInitialPolicy bool) (*networkState, error) {
	n := &networkState{path: filepath.Join(config.DataDirectory, "network.json"), cli: config.NetworkCLI, environment: config.NetworkEnvironment, discover: discoverNetworkAddresses, desktop: config.DesktopToken != ""}
	// Programmatic callers can supply explicit values without parsing CLI flags.
	if n.cli.Listen == nil && config.Listen != "" {
		n.cli.Listen = &config.Listen
	}
	if n.cli.PublicURLs == nil && config.PublicURLs != nil {
		n.cli.PublicURLs = &config.PublicURLs
	}
	if n.cli.AllowRemoteManagement == nil && config.AllowRemoteManagement {
		n.cli.AllowRemoteManagement = &config.AllowRemoteManagement
	}
	raw, err := os.ReadFile(n.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&n.saved); err != nil {
			return nil, fmt.Errorf("read network settings: %w", err)
		}
	}
	// Freeze the old Host policy before the first upgraded start. New standalone
	// Servers accept any valid hostname; Desktop keeps its local Host boundary.
	// Persisting the initial choice makes subsequent restarts unambiguous.
	var accountMarker accountInitialization
	freshAccount := ReadJSON(filepath.Join(config.DataDirectory, accountInitializationFile), &accountMarker) == nil && accountMarker.Schema == 1
	initializeHostPolicy := n.saved.RestrictHostnames == nil
	if initializeHostPolicy {
		_, databaseErr := os.Stat(filepath.Join(config.DataDirectory, "astral.db"))
		if databaseErr != nil && !errors.Is(databaseErr, os.ErrNotExist) {
			return nil, databaseErr
		}
		restricted := config.DesktopToken != "" || err == nil || (databaseErr == nil && !freshAccount)
		n.saved.RestrictHostnames = &restricted
	}
	for _, values := range []NetworkValues{n.saved, n.environment, n.cli} {
		if values.Listen != nil {
			if err := validateListen(*values.Listen); err != nil {
				return nil, err
			}
		}
		if values.PublicURLs != nil {
			normalized, err := normalizePublicURLs(*values.PublicURLs)
			if err != nil {
				return nil, err
			}
			*values.PublicURLs = normalized
		}
		if values.AllowedHostnames != nil {
			normalized, err := normalizeHostnames(*values.AllowedHostnames)
			if err != nil {
				return nil, err
			}
			*values.AllowedHostnames = normalized
		}
	}
	if initializeHostPolicy && persistInitialPolicy {
		if err := WriteJSON(n.path, n.saved); err != nil {
			return nil, err
		}
	}
	if freshAccount && persistInitialPolicy {
		if err := os.Remove(filepath.Join(config.DataDirectory, accountInitializationFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	n.activeListen = resolveString(n.cli.Listen, n.environment.Listen, n.saved.Listen, defaultListen, "default")
	return n, nil
}
func validateListen(value string) error {
	if value == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return domain.Invalid("Listen address must be an IP address and port.")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 0 || number > 65535 || (host != "" && net.ParseIP(host) == nil && host != "localhost") {
		return domain.Invalid("Listen address must be an IP address and a port between 0 and 65535.")
	}
	return nil
}
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func resolveString(cli, environment, saved *string, fallback, fallbackSource string) httpapi.NetworkStringSetting {
	result := httpapi.NetworkStringSetting{Value: fallback, SavedValue: stringValue(saved), Source: fallbackSource, Editable: true}
	for _, item := range []struct {
		value  *string
		source string
	}{{cli, "cli"}, {environment, "environment"}, {saved, "settings"}} {
		if item.value != nil {
			result.Value, result.Source = *item.value, item.source
			if result.Value == "" {
				result.Value = fallback
			}
			result.Editable = item.source == "settings"
			return result
		}
	}
	return result
}
func normalizePublicURLs(values []string) ([]string, error) {
	result := []string{}
	seen := map[string]bool{}
	for _, raw := range values {
		value := strings.TrimRight(strings.TrimSpace(raw), "/")
		if err := serviceurl.Validate(value); err != nil {
			return nil, err
		}
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result, nil
}
func (n *networkState) publicURLsLocked() httpapi.NetworkURLsSetting {
	return n.publicURLsWithLocked(n.saved)
}
func (n *networkState) publicURLsWithLocked(saved NetworkValues) httpapi.NetworkURLsSetting {
	result := httpapi.NetworkURLsSetting{Value: []string{}, SavedValue: []string{}, Source: "default", Editable: true}
	if saved.PublicURLs != nil {
		result.SavedValue = append(result.SavedValue, (*saved.PublicURLs)...)
	}
	if len(n.addresses) > 0 {
		result.Value = append(result.Value, n.addresses[0].URL)
		if n.addresses[0].Kind != "loopback" {
			result.Source = "discovery"
		}
	}
	for _, item := range []struct {
		value  *[]string
		source string
	}{{n.cli.PublicURLs, "cli"}, {n.environment.PublicURLs, "environment"}, {saved.PublicURLs, "settings"}} {
		if item.value == nil {
			continue
		}
		result.Source, result.Editable = item.source, item.source == "settings"
		if len(*item.value) > 0 {
			result.Value = append([]string{}, (*item.value)...)
		}
		break
	}
	return result
}
func (n *networkState) remoteLocked() httpapi.NetworkBoolSetting {
	if !n.desktop {
		// Standalone Servers use Web as their management entry. Only an explicit
		// operator startup override can close it; a Web preference cannot strand
		// a headless installation without its primary interface.
		value := resolveBool(n.cli.AllowRemoteManagement, n.environment.AllowRemoteManagement, nil)
		if n.saved.AllowRemoteManagement != nil {
			value.SavedValue = *n.saved.AllowRemoteManagement
		}
		if n.cli.AllowRemoteManagement == nil && n.environment.AllowRemoteManagement == nil {
			value.Value, value.Source = true, "server"
		}
		value.Editable = false
		return value
	}
	return resolveBool(n.cli.AllowRemoteManagement, n.environment.AllowRemoteManagement, n.saved.AllowRemoteManagement)
}
func resolveBool(cli, environment, saved *bool) httpapi.NetworkBoolSetting {
	value := httpapi.NetworkBoolSetting{Source: "default", Editable: true}
	if saved != nil {
		value.SavedValue = *saved
	}
	for _, item := range []struct {
		value  *bool
		source string
	}{{cli, "cli"}, {environment, "environment"}, {saved, "settings"}} {
		if item.value != nil {
			value.Value, value.Source, value.Editable = *item.value, item.source, item.source == "settings"
			break
		}
	}
	return value
}
func (n *networkState) restrictHostnamesLocked(saved NetworkValues) httpapi.NetworkBoolSetting {
	return resolveBool(n.cli.RestrictHostnames, n.environment.RestrictHostnames, saved.RestrictHostnames)
}
func (n *networkState) allowedHostnamesLocked(saved NetworkValues) httpapi.NetworkHostnamesSetting {
	value := httpapi.NetworkHostnamesSetting{Value: []string{}, SavedValue: []string{}, Source: "default", Editable: true}
	if saved.AllowedHostnames != nil {
		value.SavedValue = append(value.SavedValue, (*saved.AllowedHostnames)...)
	}
	for _, item := range []struct {
		value  *[]string
		source string
	}{{n.cli.AllowedHostnames, "cli"}, {n.environment.AllowedHostnames, "environment"}, {saved.AllowedHostnames, "settings"}} {
		if item.value != nil {
			value.Value = append(value.Value, (*item.value)...)
			value.Source, value.Editable = item.source, item.source == "settings"
			return value
		}
	}
	if n.restrictHostnamesLocked(saved).Value {
		value.Value = automaticHostnames(n.publicURLsWithLocked(saved).Value, n.addresses)
	}
	return value
}
func (n *networkState) snapshotLocked() httpapi.NetworkSettings {
	listen := n.activeListen
	listen.SavedValue = stringValue(n.saved.Listen)
	desired := resolveString(n.cli.Listen, n.environment.Listen, n.saved.Listen, defaultListen, "default")
	public := n.publicURLsLocked()
	addresses := append([]httpapi.NetworkAddress{}, n.addresses...)
	configured := make([]httpapi.NetworkAddress, 0, len(public.Value)+len(addresses))
	for _, value := range public.Value {
		configured = append(configured, httpapi.NetworkAddress{URL: value, Label: "Access address", Kind: "configured"})
	}
	addresses = append(configured, addresses...)

	// A selected discovered address must occur only once in the chooser.
	seen := map[string]bool{}
	unique := addresses[:0]
	for _, address := range addresses {
		if !seen[address.URL] {
			unique = append(unique, address)
			seen[address.URL] = true
		}
	}
	return httpapi.NetworkSettings{Listen: listen, PublicURLs: public, AllowRemoteManagement: n.remoteLocked(), RestrictHostnames: n.restrictHostnamesLocked(n.saved), AllowedHostnames: n.allowedHostnamesLocked(n.saved), Addresses: unique, RestartRequired: desired.Value != n.activeListen.Value}
}
func (n *networkState) snapshot() httpapi.NetworkSettings {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.snapshotLocked()
}
func (s *Server) PublicURL() string {
	s.network.mu.RLock()
	defer s.network.mu.RUnlock()
	values := s.network.publicURLsLocked().Value
	if len(values) > 0 {
		return values[0]
	}
	return ""
}
func (s *Server) allowsRemoteManagement() bool {
	s.network.mu.RLock()
	defer s.network.mu.RUnlock()
	return s.network.remoteLocked().Value
}
func (s *Server) NetworkSettings(ctx context.Context) httpapi.NetworkSettings {
	n := s.network
	n.mu.RLock()
	fresh := time.Since(n.discoveredAt) < 30*time.Second
	n.mu.RUnlock()
	if !fresh && s.Listener != nil {
		// Only settings/bootstrap discovery refreshes candidates. Daemon traffic
		// reads the cached effective URL and never launches a Tailscale command.
		n.discoveryMu.Lock()
		n.mu.RLock()
		fresh = time.Since(n.discoveredAt) < 30*time.Second
		n.mu.RUnlock()
		if !fresh {
			discoveryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			addresses := n.discover(discoveryCtx, s.Listener.Addr().String(), s.URL)
			cancel()
			if ctx.Err() == nil {
				n.mu.Lock()
				n.addresses, n.discoveredAt = addresses, time.Now()
				n.mu.Unlock()
			}
		}
		n.discoveryMu.Unlock()
	}
	return n.snapshot()
}
func (s *Server) SaveNetworkSettings(ctx context.Context, patch httpapi.NetworkPatch) (httpapi.NetworkSettings, error) {
	n := s.network
	n.mu.Lock()
	defer n.mu.Unlock()
	current := n.snapshotLocked()
	next := n.saved
	if patch.Listen != nil {
		if !current.Listen.Editable {
			return current, domain.Conflict("Listen address is controlled by " + current.Listen.Source + ".")
		}
		value := strings.TrimSpace(*patch.Listen)
		if err := validateListen(value); err != nil {
			return current, err
		}
		if value == "" {
			next.Listen = nil
		} else {
			next.Listen = &value
		}
	}
	if patch.PublicURLs != nil {
		if !current.PublicURLs.Editable {
			return current, domain.Conflict("Access addresses are controlled by " + current.PublicURLs.Source + ".")
		}
		values, err := normalizePublicURLs(*patch.PublicURLs)
		if err != nil {
			return current, err
		}
		if len(values) == 0 {
			next.PublicURLs = nil
		} else {
			next.PublicURLs = &values
		}
	}

	if patch.AllowRemoteManagement != nil {
		if !current.AllowRemoteManagement.Editable {
			if !n.desktop {
				return current, domain.Conflict("Standalone Server remote management is controlled by startup configuration. Use --allow-remote-management=false or ASTRAL_ALLOW_REMOTE_MANAGEMENT=false to disable it.")
			}
			return current, domain.Conflict("Remote management is controlled by " + current.AllowRemoteManagement.Source + ".")
		}
		value := *patch.AllowRemoteManagement
		next.AllowRemoteManagement = &value
	}
	if patch.RestrictHostnames != nil {
		if !current.RestrictHostnames.Editable {
			option := "--restrict-hostnames"
			if current.RestrictHostnames.Source == "environment" {
				option = "ASTRAL_RESTRICT_HOSTNAMES"
			}
			return current, domain.Conflict("Hostname restriction is controlled by " + current.RestrictHostnames.Source + " (" + option + "). Change or remove that Server startup override and restart before editing this setting.")
		}
		value := *patch.RestrictHostnames
		next.RestrictHostnames = &value
	}
	if patch.AllowedHostnames != nil {
		if !current.AllowedHostnames.Editable {
			return current, domain.Conflict("Allowed hostnames are controlled by " + current.AllowedHostnames.Source + ".")
		}
		value, err := normalizeHostnames(*patch.AllowedHostnames)
		if err != nil {
			return current, err
		}
		next.AllowedHostnames = &value
	}
	if n.restrictHostnamesLocked(next).Value && (patch.RestrictHostnames != nil || patch.AllowedHostnames != nil || patch.PublicURLs != nil) {
		allowed := n.allowedHostnamesLocked(next).Value
		if len(allowed) == 0 {
			return current, domain.Invalid("Keep at least one allowed hostname while the restriction is enabled.")
		}
		if requested := httpapi.NetworkRequestHostname(ctx); requested != "" {
			host, err := normalizeHostname(requested)
			if err != nil || !containsHostname(allowed, host) {
				return current, domain.Invalid("Keep the hostname used by this browser in the allowed list. Use astral config hostnames disable on the Server if you need to recover access.")
			}
		}
	}
	if err := WriteJSON(n.path, next); err != nil {
		return current, err
	}
	n.saved = next
	return n.snapshotLocked(), nil
}

// ContainerURL always names the main listener. An explicit loopback-only bind
// cannot accept Docker Desktop's host bridge and must not be advertised as one.
func (s *Server) ContainerURL() string {
	host, port, err := net.SplitHostPort(s.Listener.Addr().String())
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return "http://host.docker.internal:" + port
	}
	return ""
}
func loopbackRequest(raddr string) bool {
	host, _, err := net.SplitHostPort(raddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// The remote surface is a separate allowlist mux. It cannot accidentally expose
// a newly added Owner API, login route, or SPA fallback.
func (s *Server) accessHandler(management, computer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == computers.TunnelPath || r.URL.Path == "/health" {
			computer.ServeHTTP(w, r)
			return
		}
		if loopbackRequest(r.RemoteAddr) || s.allowsRemoteManagement() {
			management.ServeHTTP(w, r)
			return
		}
		computer.ServeHTTP(w, r)
	})
}

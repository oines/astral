package platform

import (
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpapi"
	"golang.org/x/net/idna"
)

func normalizeHostname(raw string) (string, error) {
	value := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	invalid := func() (string, error) {
		return "", domain.Invalid("Enter a hostname or IP address without a protocol, port, path or wildcard.")
	}
	bracketed := strings.HasPrefix(value, "[") || strings.HasSuffix(value, "]")
	if bracketed {
		if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
			return invalid()
		}
		address, err := netip.ParseAddr(value[1 : len(value)-1])
		if err != nil || !address.Is6() || address.Zone() != "" {
			return invalid()
		}
		return address.Unmap().String(), nil
	}
	if address, err := netip.ParseAddr(value); err == nil && address.Zone() == "" {
		return address.Unmap().String(), nil
	}
	if value == "" || strings.ContainsAny(value, ":/\\@?#%* \t\r\n") {
		return invalid()
	}
	value, err := idna.Lookup.ToASCII(value)
	if err != nil || len(value) > 253 {
		return invalid()
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return invalid()
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return invalid()
			}
		}
	}
	return value, nil
}

func normalizeHostnames(values []string) ([]string, error) {
	if len(values) > 128 {
		return nil, domain.Invalid("Use at most 128 allowed hostnames.")
	}
	result := []string{}
	for _, value := range values {
		host, err := normalizeHostname(value)
		if err != nil {
			return nil, err
		}
		if !containsHostname(result, host) {
			result = append(result, host)
		}
	}
	return result, nil
}

func containsHostname(values []string, requested string) bool {
	for _, value := range values {
		if value == requested {
			return true
		}
	}
	return false
}

// Existing installations and Desktop retain the previously derived local and
// configured hostnames until the owner saves an explicit list. New unrestricted
// Servers do not learn hostnames from incoming requests.
func automaticHostnames(public []string, addresses []httpapi.NetworkAddress) []string {
	result := []string{"localhost", "127.0.0.1", "::1"}
	appendURL := func(value string) {
		parsed, err := url.Parse(value)
		if err != nil {
			return
		}
		host, err := normalizeHostname(parsed.Hostname())
		if err == nil && !containsHostname(result, host) {
			result = append(result, host)
		}
	}
	for _, value := range public {
		appendURL(value)
	}
	for _, value := range addresses {
		appendURL(value.URL)
	}
	if interfaces, err := net.InterfaceAddrs(); err == nil {
		for _, item := range interfaces {
			address, _, err := net.ParseCIDR(item.String())
			if err == nil && !address.IsUnspecified() && (address.IsGlobalUnicast() || address.IsLoopback()) {
				value, _ := normalizeHostname(address.String())
				if !containsHostname(result, value) {
					result = append(result, value)
				}
			}
		}
	}
	return result
}

func (s *Server) managementHostnameAllowed(host string) bool {
	n := s.network
	n.mu.RLock()
	defer n.mu.RUnlock()
	return !n.restrictHostnamesLocked(n.saved).Value || containsHostname(n.allowedHostnamesLocked(n.saved).Value, host)
}

// ManagementOriginAllowed is an admission check for the request's own origin.
// The auth service separately compares the browser Origin to that exact origin;
// accepting a hostname never authorizes another site to call the management API.
func (s *Server) ManagementOriginAllowed(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return false
	}
	host, err := normalizeHostname(parsed.Hostname())
	return err == nil && s.managementHostnameAllowed(host)
}

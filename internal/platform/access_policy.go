package platform

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/domain"
	"golang.org/x/net/idna"
)

// RequestOrigin accepts proxy protocol information only from explicitly trusted
// peers. Proxies must preserve Host; forwarded host/address headers are ignored.
func (s *Server) RequestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if s.trustedProxy(r.RemoteAddr) && r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return canonicalOrigin(scheme + "://" + r.Host)
}

func canonicalOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil {
			return ""
		}
	}
	scheme, port := strings.ToLower(parsed.Scheme), parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host
}

func (s *Server) trustedProxy(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	address = address.Unmap()
	values := s.Config.TrustedProxies
	if values == nil {
		return address.IsLoopback()
	}
	for _, value := range values {
		if prefix, err := netip.ParsePrefix(value); err == nil && prefix.Contains(address) {
			return true
		}
		if ip, err := netip.ParseAddr(value); err == nil && ip.Unmap() == address {
			return true
		}
	}
	return false
}

func (s *Server) WebOrigins() []string {
	s.network.mu.RLock()
	values := append([]string{}, s.network.publicURLsLocked().Value...)
	s.network.mu.RUnlock()
	return s.webOriginsWith(values)
}

func (s *Server) ValidateAccessAddresses(r *http.Request, values []string) error {
	if auth.IdentityFromContext(r.Context()).Desktop {
		return nil
	}
	current := s.RequestOrigin(r)
	for _, origin := range s.webOriginsWith(values) {
		if origin == current {
			return nil
		}
	}
	return domain.Invalid("Open Astral through another allowed address before removing the address used by this browser.")
}

func (s *Server) webOriginsWith(configured []string) []string {
	values := append([]string{}, configured...)
	s.network.mu.RLock()
	for _, address := range s.network.addresses {
		values = append(values, address.URL)
	}
	s.network.mu.RUnlock()
	values = append(values, s.URL)
	_, port, _ := net.SplitHostPort(s.Listener.Addr().String())
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		values = append(values, "http://"+net.JoinHostPort(host, port))
	}
	if interfaces, err := net.InterfaceAddrs(); err == nil {
		for _, item := range interfaces {
			ip, _, err := net.ParseCIDR(item.String())
			if err == nil && !ip.IsUnspecified() && (ip.IsGlobalUnicast() || ip.IsLoopback()) {
				values = append(values, "http://"+net.JoinHostPort(ip.String(), port))
			}
		}
	}
	result := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" {
			continue
		}
		origin := canonicalOrigin(value)
		if origin == "" {
			continue
		}
		if !seen[origin] {
			result = append(result, origin)
			seen[origin] = true
		}
	}
	return result
}

func (s *Server) managementBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested, err := url.Parse("//" + r.Host)
		if err != nil || requested.User != nil || requested.Path != "" || requested.RawQuery != "" || requested.Fragment != "" {
			http.Error(w, "unrecognized management host", http.StatusForbidden)
			return
		}
		host := strings.TrimSuffix(strings.ToLower(requested.Hostname()), ".")
		for _, origin := range s.WebOrigins() {
			allowed, _ := url.Parse(origin)
			if allowed != nil && host != "" && host == strings.TrimSuffix(strings.ToLower(allowed.Hostname()), ".") {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "unrecognized management host", http.StatusForbidden)
	})
}

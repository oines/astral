// Package serviceurl validates Astral Server base URLs independently of model
// provider endpoints. Astral supports private HTTP networks and HTTPS proxies.
package serviceurl

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/oines/astral/internal/domain"
)

func Validate(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.TrimSpace(raw) != raw {
		return domain.Invalid("an Astral Server URL must contain a host, without credentials, query or fragment")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsUnspecified() || u.Hostname() == "*" {
		return domain.Invalid("an Astral Server URL must name a reachable host, not a wildcard listen address")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return domain.Invalid("an Astral Server URL port must be between 1 and 65535")
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return domain.Invalid("an Astral Server URL must use HTTP or HTTPS")
	}
	return nil
}

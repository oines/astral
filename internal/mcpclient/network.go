package mcpclient

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxURLBytes = 4096

func parseEndpoint(raw string) (*url.URL, error) {
	if len(raw) > maxURLBytes {
		return nil, ErrLimit
	}
	parsed, err := url.Parse(raw)
	if err != nil || !validEndpoint(parsed) {
		return nil, ErrDestination
	}
	return parsed, nil
}

func validEndpoint(parsed *url.URL) bool {
	return parsed != nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Hostname() != "" &&
		parsed.User == nil && parsed.Fragment == "" && parsed.Opaque == "" && !strings.ContainsRune(parsed.Hostname(), '%')
}

func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && endpointPort(a) == endpointPort(b)
}

func endpointPort(address *url.URL) string {
	if port := address.Port(); port != "" {
		return port
	}
	if address.Scheme == "https" {
		return "443"
	}
	return "80"
}

func denyRedirect(_ *http.Request, _ []*http.Request) error { return ErrDestination }

func newTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		DisableCompression:     true,
		MaxIdleConnsPerHost:    4,
		MaxConnsPerHost:        maxInFlight + 2,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 16 << 10,
	}
}

func connectionHeaders(input []Header) (http.Header, error) {
	if len(input) > 32 {
		return nil, ErrLimit
	}
	headers := make(http.Header, len(input))
	size := 0
	for _, header := range input {
		size += len(header.Name) + len(header.Value)
		if len(header.Name) > 128 || len(header.Value) > 4096 || size > 16<<10 {
			return nil, ErrLimit
		}
		name := http.CanonicalHeaderKey(header.Name)
		if !validHeaderName(name) || reservedHeader(name) || !validHeaderValue(header.Value) {
			return nil, errors.New("invalid MCP connection header")
		}
		headers.Add(name, header.Value)
	}
	return headers, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for i := range len(value) {
		if value[i] < ' ' && value[i] != '\t' || value[i] == 127 {
			return false
		}
	}
	return true
}

func reservedHeader(name string) bool {
	switch name {
	case "Accept", "Accept-Encoding", "Connection", "Content-Length", "Content-Type", "Expect", "Host", "Keep-Alive", "Last-Event-Id", "Mcp-Session-Id", "Mcp-Protocol-Version", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

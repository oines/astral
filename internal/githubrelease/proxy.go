// Package githubrelease routes only Astral's official public release downloads.
package githubrelease

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
)

const ProxyEnvironment = "ASTRAL_GITHUB_PROXY"

// ProxyOverride explicitly selects a download source for one operation. When
// Set is true and Prefix is empty, direct GitHub downloads are forced even if
// ASTRAL_GITHUB_PROXY is present in the process environment.
type ProxyOverride struct {
	Set    bool
	Prefix string
}

// NormalizeProxy accepts an explicit HTTPS URL prefix, never credentials or a
// query string. The original official URL is appended to this prefix unchanged.
func NormalizeProxy(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || strings.ContainsAny(raw, "\\?#") || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", errors.New("ASTRAL_GITHUB_PROXY must be an HTTPS URL prefix without credentials, query or fragment")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", errors.New("ASTRAL_GITHUB_PROXY has an invalid HTTPS port")
		}
	}
	return strings.TrimRight(raw, "/") + "/", nil
}

func Proxy() (string, error) { return NormalizeProxy(os.Getenv(ProxyEnvironment)) }

func ResolveProxy(override ProxyOverride) (string, error) {
	if override.Set {
		return NormalizeProxy(override.Prefix)
	}
	return Proxy()
}

// Address cannot be used to send a Server URL, enrollment token or another
// repository to a public proxy. Callers still validate versions and asset names.
func Address(official string) (string, error) {
	return AddressWithProxy(official, ProxyOverride{})
}

func AddressWithProxy(official string, override ProxyOverride) (string, error) {
	parsed, err := url.Parse(official)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("invalid official Astral release URL")
	}
	allowed := parsed.Host == "api.github.com" && parsed.Path == "/repos/oines/astral/releases/latest"
	allowed = allowed || parsed.Host == "github.com" && (strings.HasPrefix(parsed.Path, "/oines/astral/releases/download/") || parsed.Path == "/oines/astral/releases/latest/download" || strings.HasPrefix(parsed.Path, "/oines/astral/releases/latest/download/"))
	if !allowed {
		return "", errors.New("invalid official Astral release URL")
	}
	prefix, err := ResolveProxy(override)
	if err != nil || prefix == "" {
		return official, err
	}
	return prefix + official, nil
}

// CheckRedirect retains the official GitHub asset hosts and permits only the
// explicitly selected proxy's HTTPS origin in addition to them.
func CheckRedirect(request *http.Request, via []*http.Request) error {
	return checkRedirectWithProxy(request, via, ProxyOverride{})
}

func CheckRedirectWithProxy(override ProxyOverride) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		return checkRedirectWithProxy(request, via, override)
	}
}

func checkRedirectWithProxy(request *http.Request, via []*http.Request, override ProxyOverride) error {
	if len(via) >= 8 || request.URL.Scheme != "https" || request.URL.User != nil {
		return errors.New("untrusted official release redirect")
	}
	switch request.URL.Hostname() {
	case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return nil
	}
	prefix, err := ResolveProxy(override)
	if err != nil {
		return err
	}
	if prefix != "" {
		proxy, _ := url.Parse(prefix)
		if request.URL.Host == proxy.Host {
			return nil
		}
	}
	return errors.New("unexpected official release redirect host")
}

// RequestError keeps transport failures useful without including a configured
// proxy path in user-visible update state or logs.
func RequestError(err error, official string) error {
	var failure *url.Error
	if errors.As(err, &failure) {
		return &url.Error{Op: failure.Op, URL: official, Err: failure.Err}
	}
	return err
}

package githubrelease

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestProxyOnlyRoutesOfficialPublicReleaseURLs(t *testing.T) {
	const official = "https://github.com/oines/astral/releases/download/v0.1.9/astral-linux-amd64"
	t.Setenv(ProxyEnvironment, "")
	if got, err := Address(official); err != nil || got != official {
		t.Fatalf("default download is not direct: %s %v", got, err)
	}
	t.Setenv(ProxyEnvironment, "https://proxy.example/prefix///")
	if got, err := Address(official); err != nil || got != "https://proxy.example/prefix/"+official {
		t.Fatalf("explicit proxy lost original official URL: %s %v", got, err)
	}
	for _, address := range []string{"https://server.example:4545/auth/login", "https://github.com/other/project/releases/download/v1.0.0/secret", "https://api.github.com/repos/oines/astral/issues", official + "?token=secret", "http://github.com/oines/astral/releases/latest/download/astral"} {
		if _, err := Address(address); err == nil {
			t.Fatalf("proxy accepted non-public-release URL %q", address)
		}
	}
	for _, prefix := range []string{"http://proxy.example/", "https://user:password@proxy.example/", "https://proxy.example/?token=secret", "https://proxy.example/#", "https://proxy.example/?", "https://proxy.example/a\nb", "https://proxy.example:65536/", "https://proxy.example:0/", "https://proxy.example/a b"} {
		t.Setenv(ProxyEnvironment, prefix)
		if _, err := Address(official); err == nil || strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid proxy did not fail safely: %v", err)
		}
	}
}

func TestProxyRedirectsRemainHTTPSAndConfinedToExplicitHost(t *testing.T) {
	t.Setenv(ProxyEnvironment, "https://proxy.example:8443/prefix/")
	for _, address := range []string{"https://proxy.example:8443/next", "https://github.com/oines/astral/releases/download/v0.1.9/asset", "https://release-assets.githubusercontent.com/asset?signature=public"} {
		r, _ := http.NewRequest(http.MethodGet, address, nil)
		if err := CheckRedirect(r, nil); err != nil {
			t.Errorf("trusted redirect rejected: %s %v", address, err)
		}
	}
	for _, address := range []string{"http://proxy.example:8443/next", "https://proxy.example/next", "https://unselected-proxy.example/next", "https://user@proxy.example:8443/next"} {
		r, _ := http.NewRequest(http.MethodGet, address, nil)
		if err := CheckRedirect(r, nil); err == nil {
			t.Errorf("untrusted redirect accepted: %s", address)
		}
	}
	r, _ := http.NewRequest(http.MethodGet, "https://proxy.example:8443/next", nil)
	if err := CheckRedirect(r, make([]*http.Request, 8)); err == nil {
		t.Fatal("redirect limit was removed")
	}
	t.Setenv(ProxyEnvironment, "")
	if err := CheckRedirect(r, nil); err == nil {
		t.Fatal("proxy redirect accepted without explicit configuration")
	}
}

func TestRequestErrorDoesNotExposeProxyPath(t *testing.T) {
	err := RequestError(&url.Error{Op: "Get", URL: "https://proxy.example/private-path/https://github.com/oines/astral", Err: errors.New("connection refused")}, "https://github.com/oines/astral")
	if strings.Contains(err.Error(), "private-path") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("unexpected error redaction: %v", err)
	}
}

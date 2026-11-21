package releasefeed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/oines/astral/internal/githubrelease"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLatestPrefersStandaloneDaemonAndSupportsProxyOverride(t *testing.T) {
	daemonBytes := []byte("standalone-daemon-binary")
	daemonSum := sha256.Sum256(daemonBytes)
	daemonSHA := hex.EncodeToString(daemonSum[:])
	fullBytes := []byte("full-astral-binary")
	fullSum := sha256.Sum256(fullBytes)
	fullSHA := hex.EncodeToString(fullSum[:])

	var requestedURLs []string
	c := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requestedURLs = append(requestedURLs, r.URL.String())
		body := ""
		switch {
		case strings.HasSuffix(r.URL.String(), "/releases/latest"):
			body = `{"tag_name":"v0.2.0","draft":false,"prerelease":false}`
		case strings.HasSuffix(r.URL.String(), "/checksums.txt"):
			body = fullSHA + "  astral-windows-amd64.exe\n" + daemonSHA + "  astral-daemon-windows-amd64.exe\n"
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	t.Setenv(githubrelease.ProxyEnvironment, "https://env-proxy.example/")

	// Explicit proxy override wins over environment variable.
	bin, err := latestWithProxy(context.Background(), "windows", "amd64", c, githubrelease.ProxyOverride{Set: true, Prefix: "https://edgeone.gh-proxy.org"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if bin.Name != "astral-daemon-windows-amd64.exe" || bin.SHA256 != daemonSHA || bin.Version != "0.2.0" {
		t.Fatalf("unexpected binary descriptor: %+v", bin)
	}
	for _, u := range requestedURLs {
		if !strings.HasPrefix(u, "https://edgeone.gh-proxy.org/") {
			t.Fatalf("expected override proxy prefix, got %s", u)
		}
	}

	unified, err := latestWithProxy(context.Background(), "windows", "amd64", c, githubrelease.ProxyOverride{Set: true}, false)
	if err != nil || unified.Name != "astral-windows-amd64.exe" || unified.SHA256 != fullSHA {
		t.Fatalf("legacy Daemon cannot consume selected release: %+v, %v", unified, err)
	}

	// Explicit empty override forces direct GitHub even when env var is set.
	requestedURLs = nil
	bin, err = latestWithProxy(context.Background(), "windows", "amd64", c, githubrelease.ProxyOverride{Set: true, Prefix: ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range requestedURLs {
		if strings.Contains(u, "env-proxy.example") {
			t.Fatalf("explicit direct override leaked env proxy: %s", u)
		}
	}

	// Falls back to full astral binary when checksums.txt predates standalone daemon.
	legacyClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"tag_name":"v0.1.9","draft":false,"prerelease":false}`
		if strings.HasSuffix(r.URL.String(), "/checksums.txt") {
			body = fullSHA + "  astral-linux-amd64\n"
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	legacy, err := latestWithProxy(context.Background(), "linux", "amd64", legacyClient, githubrelease.ProxyOverride{Set: true, Prefix: ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Name != "astral-linux-amd64" || legacy.SHA256 != fullSHA {
		t.Fatalf("expected fallback to full binary, got %+v", legacy)
	}
}

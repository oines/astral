package platform

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDistributionDownloadChecksBytesBeforeReplacingExecutable(t *testing.T) {
	const name = "astral-linux-arm64"
	binary := []byte("release binary")
	digest := sha256.Sum256(binary)
	corrupt := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			fmt.Fprintf(w, "%x  %s\n", digest, name)
		case "/" + name:
			if corrupt {
				w.Write([]byte("interrupted or tampered"))
			} else {
				w.Write(binary)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), name)
	if err := downloadReleaseBinary(context.Background(), server.URL, name, target, server.Client()); err != nil {
		t.Fatal(err)
	}
	corrupt = true
	if err := downloadReleaseBinary(context.Background(), server.URL, name, target, server.Client()); err == nil {
		t.Fatal("accepted mismatched distribution")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != string(binary) {
		t.Fatalf("corrupt download replaced the working executable: %q, %v", data, err)
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		t.Fatalf("unexpected executable permissions: %v", err)
	}
}

func TestContainerBinaryDownloadUsesExplicitProxyAndStillVerifiesBytes(t *testing.T) {
	const name = "astral-linux-arm64"
	binary := []byte("official proxied Linux distribution")
	digest := sha256.Sum256(binary)
	corrupt := false
	calls := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/proxy/" + officialReleaseBase + "/checksums.txt":
			fmt.Fprintf(w, "%x  %s\n", digest, name)
		case "/proxy/" + officialReleaseBase + "/" + name:
			if corrupt {
				_, _ = w.Write([]byte("tampered"))
			} else {
				_, _ = w.Write(binary)
			}
		default:
			t.Errorf("download used unexpected proxy target: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("ASTRAL_GITHUB_PROXY", server.URL+"/proxy/")
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	target := filepath.Join(t.TempDir(), name)
	if err := downloadVerifiedBinary(context.Background(), name, target); err != nil {
		t.Fatal(err)
	}
	corrupt = true
	if err := downloadVerifiedBinary(context.Background(), name, target); err == nil {
		t.Fatal("proxy bypassed distribution checksum verification")
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != string(binary) || len(calls) != 4 {
		t.Fatalf("proxy failure replaced installed bytes or changed download count: %q %v %v", raw, err, calls)
	}
	t.Setenv("ASTRAL_GITHUB_PROXY", "http://untrusted.example/")
	if err := downloadVerifiedBinary(context.Background(), name, target); err == nil || strings.Contains(err.Error(), "untrusted.example") || len(calls) != 4 {
		t.Fatalf("invalid proxy did not fail before download: %v", err)
	}
}

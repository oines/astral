package platform

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestServerShutdownCancelsDesktopAuthEventStream(t *testing.T) {
	root := t.TempDir()
	s, err := newAuthenticatedTestServer(Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers"), NoLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("GET", s.URL+"/api/v1/auth/events", nil)
	request.Header.Set("Authorization", "Desktop "+testDesktopToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	if !scanner.Scan() || scanner.Text() != "event: ready" {
		t.Fatal("Desktop auth event stream did not open")
	}
	ended := make(chan struct{})
	go func() {
		for scanner.Scan() {
		}
		close(ended)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.Close(ctx); err != nil {
		t.Fatalf("active auth SSE blocked Server shutdown: %v", err)
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("auth SSE survived Server shutdown")
	}
}
func TestManagementOriginsCanonicalizeDefaultPorts(t *testing.T) {
	root := t.TempDir()
	s, err := newAuthenticatedTestServer(Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers"), NoLocal: true, PublicURLs: []string{"https://astral.example:443", "http://plain.example:80", "https://custom.example:8443"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	origins := s.WebOrigins()
	for _, expected := range []string{"https://astral.example", "http://plain.example", "https://custom.example:8443"} {
		if !slices.Contains(origins, expected) {
			t.Errorf("allowed origins are not browser-canonical; missing %q in %v", expected, origins)
		}
	}
	request := httptest.NewRequest("POST", "http://astral.example:443/auth/login/passkey/begin", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("Origin", "https://astral.example")
	if actual := s.RequestOrigin(request); actual != "https://astral.example" {
		t.Errorf("trusted-proxy request origin is not canonical: %q", actual)
	}
	if err = s.Auth.CheckOrigin(request); err != nil {
		t.Errorf("normal browser Origin rejected with explicit default port: %v", err)
	}
	if !s.Auth.PasskeyAvailable(request) {
		t.Error("configured HTTPS domain with :443 disables Passkey")
	}
}
func TestManagementHostPolicyIsReadyBeforeFirstRequestAndIgnoresForwardedHost(t *testing.T) {
	root := t.TempDir()
	s, err := newAuthenticatedTestServer(Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers"), NoLocal: true, AllowRemoteManagement: true, PublicURLs: []string{"https://astral.example"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	for _, test := range []struct {
		host, forwarded string
		status          int
	}{{"astral.example", "", 200}, {"evil.example", "astral.example", 403}} {
		request := httptest.NewRequest("GET", "http://"+test.host+"/auth/status", nil)
		request.RemoteAddr = "192.0.2.1:12345"
		request.Header.Set("X-Forwarded-Host", test.forwarded)
		response := httptest.NewRecorder()
		s.HTTP.Handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("host %q forwarded %q => %d %s", test.host, test.forwarded, response.Code, response.Body.String())
		}
	}
}

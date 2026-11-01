package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/httpapi"
	"github.com/oines/astral/internal/persistence"
)

func hostnameServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	s, err := New(Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers"), NoLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if err = s.Auth.Setup(context.Background(), "owner", "strong-test-password"); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	return s
}

func hostnameRequest(s *Server, method, host, path, origin, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.20:12345"
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if token != "" {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.HTTP.Handler.ServeHTTP(w, r)
	return w
}

func TestNewServerAcceptsUnconfiguredHostnameButStillRequiresAuthenticationAndSameOrigin(t *testing.T) {
	s := hostnameServer(t)
	if s.network.snapshot().RestrictHostnames.Value {
		t.Fatal("fresh standalone Server still requires a configured hostname")
	}
	if remote := s.network.snapshot().AllowRemoteManagement; !remote.Value || remote.Editable || remote.Source != "server" {
		t.Fatalf("standalone Server did not enable Web management as a deployment default: %+v", remote)
	}
	login := hostnameRequest(s, "POST", "new.example:4545", "/auth/login", "http://new.example:4545", "", `{"username":"owner","password":"strong-test-password"}`)
	if login.Code != 200 || len(login.Result().Cookies()) != 1 {
		t.Fatalf("unconfigured same-origin login failed: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	if cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode || !cookie.HttpOnly {
		t.Fatalf("hostname change broadened session Cookie scope: %+v", cookie)
	}
	if w := hostnameRequest(s, "GET", "new.example:4545", "/api/v1/bootstrap", "", "", ""); w.Code != 401 {
		t.Fatalf("unconfigured hostname granted anonymous management: %d %s", w.Code, w.Body.String())
	}
	if w := hostnameRequest(s, "GET", "new.example:4545", "/api/v1/bootstrap", "http://attacker.example", cookie.Value, ""); w.Code != 403 {
		t.Fatalf("cross-origin authenticated request admitted: %d %s", w.Code, w.Body.String())
	}
	request := httptest.NewRequest("GET", "http://new.example:4545/auth/status", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Forwarded-Proto", "https")
	if !s.Auth.PasskeyAvailable(request) {
		t.Fatal("HTTPS domain requires preconfigured access URL for Passkey")
	}
	request.Header.Del("X-Forwarded-Proto")
	if s.Auth.PasskeyAvailable(request) {
		t.Fatal("plain HTTP admitted Passkey")
	}
	if n, err := newNetworkState(Config{DataDirectory: s.Config.DataDirectory}); err != nil || n.snapshot().RestrictHostnames.Value {
		t.Fatalf("new Server changed hostname default after its database was created: %v", err)
	}
}

func TestWebHostnameEditsPreventSelfLockoutAndPinnedLocalControlRecoversMisconfiguration(t *testing.T) {
	s := hostnameServer(t)
	_, token, err := s.Auth.Login(context.Background(), "owner", "strong-test-password", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	w := hostnameRequest(s, "PATCH", "astral.example:4545", "/api/v1/settings/network", "http://astral.example:4545", token, `{"restrict_hostnames":true,"allowed_hostnames":["ASTRAL.Example."]}`)
	if w.Code != 200 {
		t.Fatalf("could not enable own hostname: %d %s", w.Code, w.Body.String())
	}
	if w := hostnameRequest(s, "GET", "other.example:4545", "/auth/status", "", "", ""); w.Code != 403 {
		t.Fatalf("restricted hostname admitted: %d", w.Code)
	}
	w = hostnameRequest(s, "PATCH", "astral.example:4545", "/api/v1/settings/network", "http://astral.example:4545", token, `{"allowed_hostnames":["other.example"]}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Keep the hostname") {
		t.Fatalf("Web edit locked out current browser: %d %s", w.Code, w.Body.String())
	}
	if !reflect.DeepEqual(s.network.snapshot().AllowedHostnames.Value, []string{"astral.example"}) {
		t.Fatal("rejected edit changed the effective hostname list")
	}
	// A manual configuration-file/startup mistake can still block every public
	// Web entry. Recovery must use the separate pinned administrative listener.
	if _, err = s.SaveNetworkSettings(context.Background(), httpapi.NetworkPatch{AllowedHostnames: pointer([]string{"unreachable.example"})}); err != nil {
		t.Fatal(err)
	}
	if w := hostnameRequest(s, "GET", "astral.example:4545", "/auth/status", "", "", ""); w.Code != 403 {
		t.Fatal("test did not simulate an inaccessible Web entry")
	}
	settings, err := LocalHostnameSettings(context.Background(), s.Config.DataDirectory, true)
	if err != nil || settings.RestrictHostnames.Value || !reflect.DeepEqual(settings.AllowedHostnames.Value, []string{"unreachable.example"}) {
		t.Fatalf("local recovery failed or discarded the previous list: %+v %v", settings, err)
	}
	if w := hostnameRequest(s, "GET", "astral.example:4545", "/auth/status", "", "", ""); w.Code != 200 {
		t.Fatalf("recovered hostname not immediately admitted: %d", w.Code)
	}
	if w := hostnameRequest(s, "GET", "astral.example:4545", "/api/v1/bootstrap", "", "", ""); w.Code != 401 {
		t.Fatal("hostname recovery disabled account authentication")
	}
	if status, err := AdministratorStatus(context.Background(), s.Config.DataDirectory); err != nil || !status.Configured {
		t.Fatalf("local status cannot inspect configured live Server: %+v %v", status, err)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = LocalHostnameSettings(context.Background(), s.Config.DataDirectory, true); err != nil {
		t.Fatalf("offline recovery failed: %v", err)
	}
}

func TestLegacyAndDesktopHostnameRestrictionsRemainEnabled(t *testing.T) {
	for _, desktop := range []bool{false, true} {
		directory := t.TempDir()
		config := Config{DataDirectory: directory}
		if desktop {
			config.DesktopToken = "private-desktop-test"
		} else {
			store, err := persistence.Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			_ = store.Close()
		}
		network, err := newNetworkState(config)
		if err != nil || !network.snapshot().RestrictHostnames.Value {
			t.Fatalf("existing/Desktop policy silently relaxed: %v", err)
		}
		if !containsHostname(network.snapshot().AllowedHostnames.Value, "localhost") {
			t.Fatal("default local hostname disappeared")
		}
	}
}

func TestLegacyPublicAddressEditCannotRemoveTheCurrentDerivedHostname(t *testing.T) {
	s := hostnameServer(t)
	if _, err := s.SaveNetworkSettings(context.Background(), httpapi.NetworkPatch{RestrictHostnames: pointer(true), PublicURLs: pointer([]string{"http://legacy.example:4545"})}); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Auth.Login(context.Background(), "owner", "strong-test-password", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	w := hostnameRequest(s, "PATCH", "legacy.example:4545", "/api/v1/settings/network", "http://legacy.example:4545", token, `{"public_urls":[]}`)
	if w.Code != 400 || !containsHostname(s.network.snapshot().AllowedHostnames.Value, "legacy.example") {
		t.Fatalf("legacy compatibility setting locked out current Web entry: %d %s", w.Code, w.Body.String())
	}
}

func TestHostnameOverridesCannotBeReportedAsRecoveredByChangingSavedValue(t *testing.T) {
	network, err := newNetworkState(Config{DataDirectory: t.TempDir(), NetworkCLI: NetworkValues{RestrictHostnames: pointer(true), AllowedHostnames: pointer([]string{"controlled.example"})}})
	if err != nil {
		t.Fatal(err)
	}
	server := Server{network: network}
	if _, err = server.SaveNetworkSettings(context.Background(), httpapi.NetworkPatch{RestrictHostnames: pointer(false)}); err == nil || !strings.Contains(err.Error(), "controlled by cli") {
		t.Fatalf("recovery hid a CLI override: %v", err)
	}
	if !network.snapshot().RestrictHostnames.Value {
		t.Fatal("CLI override lost priority")
	}
}

func TestHostnameNormalizationRejectsURLsPortsAndWildcards(t *testing.T) {
	for raw, want := range map[string]string{" Astral.Example. ": "astral.example", "[2001:0db8::1]": "2001:db8::1", "::ffff:127.0.0.1": "127.0.0.1", "例子.测试": "xn--fsqu00a.xn--0zwm56d"} {
		if got, err := normalizeHostname(raw); err != nil || got != want {
			t.Errorf("%q: %q %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "https://astral.example", "astral.example:4545", "*.example", "astral.example/path", "[astral.example]", "[127.0.0.1]", "::1%eth0", "a..example"} {
		if got, err := normalizeHostname(raw); err == nil {
			t.Errorf("accepted invalid hostname %q as %q", raw, got)
		}
	}
	values, err := normalizeHostnames([]string{"ASTRAL.example.", "astral.example"})
	if err != nil || !reflect.DeepEqual(values, []string{"astral.example"}) {
		t.Fatalf("duplicate canonical hostnames retained: %+v %v", values, err)
	}
}

func TestLocalHostnameControlRejectsUntrustedRequestsAndDoesNotExposeWebRoutes(t *testing.T) {
	s := hostnameServer(t)
	var info localControlInfo
	if err := ReadJSON(filepath.Join(s.Config.DataDirectory, "control.json"), &info); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveNetworkSettings(context.Background(), httpapi.NetworkPatch{RestrictHostnames: pointer(true), AllowedHostnames: pointer([]string{"public.example"})}); err != nil {
		t.Fatal(err)
	}
	w := hostnameRequest(s, "POST", "public.example", "/hostnames/disable", "http://public.example", "", "")
	if !s.network.snapshot().RestrictHostnames.Value || strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatal("public SPA fallback exposed the local configuration operation")
	}
	info.ServerPin = strings.Repeat("0", 64)
	if err := WriteJSON(filepath.Join(s.Config.DataDirectory, "control.json"), info); err != nil {
		t.Fatal(err)
	}
	if _, err := LocalHostnameSettings(context.Background(), s.Config.DataDirectory, true); err == nil {
		t.Fatal("wrong pinned identity changed live Server settings")
	}
	if status, err := AdministratorStatus(context.Background(), s.Config.DataDirectory); err == nil {
		raw, _ := json.Marshal(status)
		t.Fatalf("wrong pinned identity read live account state: %s", raw)
	}
}

func TestWebHostnameAndRemoteManagementPoliciesDoNotFilterTheComputerTunnel(t *testing.T) {
	for _, remote := range []bool{false, true} {
		network, err := newNetworkState(Config{DataDirectory: t.TempDir(), NetworkCLI: NetworkValues{AllowRemoteManagement: pointer(remote), RestrictHostnames: pointer(true), AllowedHostnames: pointer([]string{"web.example"})}})
		if err != nil {
			t.Fatal(err)
		}
		server := &Server{network: network}
		management := server.managementBoundary(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(201) }))
		computer := http.NewServeMux()
		// The production handler performs its own pinned tunnel authentication.
		// This routing test only proves that management policy never handles it.
		computer.HandleFunc("GET "+computers.TunnelPath, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
		computer.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
		handler := server.accessHandler(management, computer)
		for _, path := range []string{computers.TunnelPath, "/health"} {
			r := httptest.NewRequest("GET", "http://daemon-only.example"+path, nil)
			r.RemoteAddr = "192.0.2.30:12345"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 204 {
				t.Fatalf("remote=%t: management policy blocked Computer route %s: %d", remote, path, w.Code)
			}
		}
		r := httptest.NewRequest("GET", "http://daemon-only.example/api/v1/bootstrap", nil)
		r.RemoteAddr = "192.0.2.30:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := 404
		if remote {
			want = 403
		}
		if w.Code != want {
			t.Fatalf("remote=%t: management API bypassed its boundary: %d", remote, w.Code)
		}
	}
}

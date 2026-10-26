package platform

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/httpapi"
)

func pointer[T any](v T) *T { return &v }
func TestNetworkPriorityPreservesFalseAndSavedPreferences(t *testing.T) {
	directory := t.TempDir()
	saved := NetworkValues{Listen: pointer("127.0.0.1:4546"), PublicURLs: pointer([]string{"http://saved.example:4546"}), AllowRemoteManagement: pointer(true)}
	if err := WriteJSON(filepath.Join(directory, "network.json"), saved); err != nil {
		t.Fatal(err)
	}
	n, err := newNetworkState(Config{DataDirectory: directory, NetworkCLI: NetworkValues{PublicURLs: pointer([]string{"http://cli.example:4546"}), AllowRemoteManagement: pointer(false)}, NetworkEnvironment: NetworkValues{Listen: pointer("0.0.0.0:4547"), PublicURLs: pointer([]string{"http://env.example:4547"}), AllowRemoteManagement: pointer(true)}})
	if err != nil {
		t.Fatal(err)
	}
	n.addresses = []httpapi.NetworkAddress{{URL: "http://100.64.0.1:4547", Kind: "tailscale-ip"}}
	state := n.snapshot()
	if state.Listen.Value != "0.0.0.0:4547" || state.Listen.Source != "environment" || state.PublicURLs.Value[0] != "http://cli.example:4546" || state.PublicURLs.Source != "cli" || state.AllowRemoteManagement.Value || state.AllowRemoteManagement.Source != "cli" {
		t.Fatalf("incorrect priority: %+v", state)
	}
	if state.PublicURLs.Editable || state.AllowRemoteManagement.Editable || !state.AllowRemoteManagement.SavedValue || !reflect.DeepEqual(state.PublicURLs.SavedValue, *saved.PublicURLs) {
		t.Fatalf("overrides lost saved preference: %+v", state)
	}
	server := &Server{network: n}
	if _, err = server.SaveNetworkSettings(context.Background(), httpapi.NetworkPatch{AllowRemoteManagement: pointer(true)}); err == nil {
		t.Fatal("overridden setting accepted a write")
	}
	var unchanged NetworkValues
	if err = ReadJSON(n.path, &unchanged); err != nil || !*unchanged.AllowRemoteManagement || !reflect.DeepEqual(*unchanged.PublicURLs, *saved.PublicURLs) {
		t.Fatalf("rejected write changed persistent values: %+v %v", unchanged, err)
	}
}
func TestNetworkEmptyOverrideSelectsAutomaticAndSettingsApplyAtomically(t *testing.T) {
	directory := t.TempDir()
	n, err := newNetworkState(Config{DataDirectory: directory, NetworkCLI: NetworkValues{PublicURLs: pointer([]string{})}})
	if err != nil {
		t.Fatal(err)
	}
	n.addresses = []httpapi.NetworkAddress{{URL: "http://node.tailnet.ts.net:4545", Kind: "tailscale-dns"}}
	if state := n.snapshot(); state.PublicURLs.Source != "cli" || state.PublicURLs.Editable || state.PublicURLs.Value[0] != n.addresses[0].URL {
		t.Fatalf("empty CLI override did not choose auto: %+v", state)
	}
	n.cli = NetworkValues{}
	server := &Server{network: n}
	state, err := server.SaveNetworkSettings(context.Background(), httpapi.NetworkPatch{Listen: pointer("127.0.0.1:4550"), PublicURLs: pointer([]string{"http://custom.example:4545/"}), AllowRemoteManagement: pointer(true)})
	if err != nil {
		t.Fatal(err)
	}
	if state.Listen.Value != defaultListen || !state.RestartRequired || state.PublicURLs.Value[0] != "http://custom.example:4545" || !state.AllowRemoteManagement.Value {
		t.Fatalf("incorrect live/pending values: %+v", state)
	}
	// A failing field validates the complete patch before any disk or live change.
	if _, err = server.SaveNetworkSettings(context.Background(), httpapi.NetworkPatch{PublicURLs: pointer([]string{"ftp://bad"}), AllowRemoteManagement: pointer(false)}); err == nil {
		t.Fatal("invalid URL accepted")
	}
	if !n.snapshot().AllowRemoteManagement.Value {
		t.Fatal("partial patch applied after validation failure")
	}
	state, err = server.SaveNetworkSettings(context.Background(), httpapi.NetworkPatch{PublicURLs: pointer([]string{}), AllowRemoteManagement: pointer(false)})
	if err != nil || state.PublicURLs.Value[0] != n.addresses[0].URL || state.PublicURLs.Source != "discovery" || state.AllowRemoteManagement.Value || state.AllowRemoteManagement.Source != "settings" {
		t.Fatalf("reset/explicit false failed: %+v %v", state, err)
	}
	reopened, err := newNetworkState(Config{DataDirectory: directory})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.activeListen.Value != "127.0.0.1:4550" || reopened.snapshot().RestartRequired || reopened.snapshot().AllowRemoteManagement.Value {
		t.Fatalf("saved settings did not survive restart: %+v", reopened.snapshot())
	}
}
func TestNetworkRemoteSurfaceUsesTCPPeerOnly(t *testing.T) {
	n, err := newNetworkState(Config{DataDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{network: n}
	management := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	computers := http.NewServeMux()
	computers.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) })
	computers.HandleFunc("POST /internal/enrollments/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(203) })
	handler := server.accessHandler(management, computers)
	for _, test := range []struct {
		peer, method, path string
		want               int
	}{
		{"127.0.0.1:45", "GET", "/api/v1/bootstrap", 201},
		{"[::1]:45", "POST", "/auth/session", 201},
		{"192.168.1.2:45", "GET", "/api/v1/bootstrap", 404},
		{"192.168.1.2:45", "POST", "/auth/session", 404},
		{"192.168.1.2:45", "GET", "/openapi.json", 404},
		{"192.168.1.2:45", "GET", "/settings/security", 404},
		{"192.168.1.2:45", "GET", "/health", 202},
		{"192.168.1.2:45", "POST", "/internal/enrollments/test", 203},
		{"localhost:45", "GET", "/api/v1/bootstrap", 404},
	} {
		r := httptest.NewRequest(test.method, "http://127.0.0.1:4545"+test.path, nil)
		r.RemoteAddr = test.peer
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		r.Header.Set("X-Real-IP", "127.0.0.1")
		r.Header.Set("Origin", "http://127.0.0.1:4545")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Errorf("%s %s %s: got %d want %d", test.peer, test.method, test.path, w.Code, test.want)
		}
	}
	if _, err = server.SaveNetworkSettings(context.Background(), httpapi.NetworkPatch{AllowRemoteManagement: pointer(true)}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://astral.example/api/v1/bootstrap", nil)
	r.RemoteAddr = "100.64.1.2:43"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatal("enabling remote management did not switch the entire management surface")
	}
}
func TestNetworkActualRoutesKeepRemoteOwnerAuthentication(t *testing.T) {
	server, err := newAuthenticatedTestServer(Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(t.TempDir(), "server"), ComputerDirectory: filepath.Join(t.TempDir(), "computers"), NoLocal: true, PublicURLs: []string{"http://astral.example"}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	for _, path := range []string{"/", "/settings/security", "/api/v1/bootstrap", "/api/v1/settings/network", "/openapi.json", "/auth/session", "/internal/enrollments/x/container-access"} {
		r := httptest.NewRequest("GET", "http://astral.example"+path, nil)
		r.RemoteAddr = "192.168.1.2:1234"
		r.Header.Set("Authorization", "Desktop "+testDesktopToken)
		w := httptest.NewRecorder()
		server.HTTP.Handler.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Errorf("remote management leaked %s: %d", path, w.Code)
		}
	}
	for _, test := range []struct {
		method, path, body string
		want               int
	}{{"GET", "/health", "", 200}, {"GET", "/install.sh", "", 404}, {"GET", "/install.ps1", "", 404}, {"POST", "/internal/enrollments/missing", "{}", 404}, {"POST", "/internal/enrollments/missing/prepare", "{}", 404}} {
		r := httptest.NewRequest(test.method, "http://astral.example"+test.path, strings.NewReader(test.body))
		r.RemoteAddr = "192.168.1.2:1234"
		w := httptest.NewRecorder()
		server.HTTP.Handler.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Errorf("remote Computer route %s: got %d want %d body %s", test.path, w.Code, test.want, w.Body.String())
		}
	}
	_, err = server.SaveNetworkSettings(context.Background(), httpapi.NetworkPatch{AllowRemoteManagement: pointer(true)})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://astral.example/api/v1/settings/network", nil)
	r.RemoteAddr = "192.168.1.2:1234"
	w := httptest.NewRecorder()
	server.HTTP.Handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("remote management bypassed Owner auth: %d", w.Code)
	}
	if err = server.Auth.Setup(context.Background(), "test-admin", "test-password-123"); err != nil {
		t.Fatal(err)
	}
	_, token, loginErr := server.Auth.Login(context.Background(), "test-admin", "test-password-123", "test", "test-browser")
	if loginErr != nil {
		t.Fatal(loginErr)
	}
	r.AddCookie(&http.Cookie{Name: "astral_session", Value: token})
	w = httptest.NewRecorder()
	server.HTTP.Handler.ServeHTTP(w, r)
	var settings httpapi.NetworkSettings
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &settings) != nil || !settings.AllowRemoteManagement.Value {
		t.Fatalf("authenticated remote settings failed: %d %s", w.Code, w.Body.String())
	}
}

func TestNetworkDiscoveryRefreshIsBoundedToSettingsAndShared(t *testing.T) {
	n, err := newNetworkState(Config{DataDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var calls atomic.Int32
	n.discover = func(context.Context, string, string) []httpapi.NetworkAddress {
		calls.Add(1)
		return []httpapi.NetworkAddress{{URL: "https://updated.tailnet.ts.net", Kind: "tailscale-serve"}}
	}
	server := &Server{network: n, Listener: listener, URL: "http://" + listener.Addr().String()}
	for range 10 {
		_ = server.PublicURL()
		_ = server.allowsRemoteManagement()
	}
	if calls.Load() != 0 {
		t.Fatal("transport reads triggered discovery")
	}
	var workers sync.WaitGroup
	for range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			state := server.NetworkSettings(context.Background())
			if state.PublicURLs.Value[0] != "https://updated.tailnet.ts.net" {
				t.Errorf("refresh returned stale address: %+v", state)
			}
		}()
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent discovery calls were not shared: %d", calls.Load())
	}
	n.mu.Lock()
	n.discoveredAt = time.Now().Add(-31 * time.Second)
	n.mu.Unlock()
	_ = server.NetworkSettings(context.Background())
	if calls.Load() != 2 {
		t.Fatal("expired discovery did not refresh")
	}
}
func TestNetworkSpecificInterfaceRetainsLoopbackManagement(t *testing.T) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var host string
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() && !ip.IsLoopback() {
			host = ip.String()
			break
		}
	}
	if host == "" {
		t.Skip("no usable local interface")
	}
	directory := t.TempDir()
	server, err := newAuthenticatedTestServer(Config{Listen: net.JoinHostPort(host, "0"), DataDirectory: filepath.Join(directory, "server"), ComputerDirectory: filepath.Join(directory, "computers"), NoLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	if server.loopbackListener == nil || !strings.HasPrefix(server.URL, "http://127.0.0.1:") {
		t.Fatalf("specific bind lost local management: %s", server.URL)
	}
	if err = server.Start(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	request, err := http.NewRequest("GET", server.URL+"/api/v1/settings/network", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Desktop "+testDesktopToken)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("loopback management: got %d", response.StatusCode)
	}
	// Local firewall policy can prevent this test process from dialing its own
	// NIC. Exercise the peer classification with that actual interface address.
	remote := httptest.NewRequest("GET", "http://"+server.Listener.Addr().String()+"/api/v1/settings/network", nil)
	remote.RemoteAddr = net.JoinHostPort(host, "12345")
	remote.Header.Set("Authorization", "Desktop "+testDesktopToken)
	output := httptest.NewRecorder()
	server.HTTP.Handler.ServeHTTP(output, remote)
	if output.Code != 404 {
		t.Fatalf("specific NIC admitted management: %d", output.Code)
	}
}

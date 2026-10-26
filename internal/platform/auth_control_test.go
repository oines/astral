package platform

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/securetransport"
)

func controlTestServer(t *testing.T) *Server {
	t.Helper()
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
	return s
}
func TestLocalControlUsesPinnedHTTPSAndCanConfigureRunningServer(t *testing.T) {
	s := controlTestServer(t)
	var info localControlInfo
	if err := ReadJSON(filepath.Join(s.Config.DataDirectory, "control.json"), &info); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.URL, "https://127.0.0.1:") || info.ServerPin != s.Hub.ServerPin() {
		t.Fatalf("private control is not pinned HTTPS: %+v", info)
	}
	if err := ConfigureAdministrator(context.Background(), s.Config.DataDirectory, "owner", "correct-horse-battery", false); err != nil {
		t.Fatal(err)
	}
	result, oldToken, err := s.Auth.Login(context.Background(), "owner", "correct-horse-battery", "test", "")
	if err != nil || !result.Authenticated {
		t.Fatalf("configured account did not authenticate: %+v %v", result, err)
	}
	if err = ConfigureAdministrator(context.Background(), s.Config.DataDirectory, "restored", "new-secure-password", true); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", s.URL+"/api/v1/auth", nil)
	request.AddCookie(&http.Cookie{Name: "astral_session", Value: oldToken})
	if _, err = s.Auth.Authenticate(request); err == nil {
		t.Fatal("local reset left old browser token valid")
	}
}
func TestLocalControlRejectsWrongPinWhileLiveServerHoldsDataLock(t *testing.T) {
	s := controlTestServer(t)
	path := filepath.Join(s.Config.DataDirectory, "control.json")
	var info localControlInfo
	if err := ReadJSON(path, &info); err != nil {
		t.Fatal(err)
	}
	info.ServerPin = strings.Repeat("0", 64)
	if err := WriteJSON(path, info); err != nil {
		t.Fatal(err)
	}
	err := ConfigureAdministrator(context.Background(), s.Config.DataDirectory, "intruder", "strong-test-password", false)
	if err == nil {
		t.Fatal("wrong pin configured a running Server")
	}
	status, _ := s.Auth.Status(context.Background())
	if status.Configured {
		t.Fatal("wrong pin delivered account credentials")
	}
	var unchanged localControlInfo
	if err = ReadJSON(path, &unchanged); err != nil || unchanged.ServerPin != info.ServerPin {
		t.Fatalf("failed live request removed/rewrote active control record: %v", err)
	}
}
func TestStaleControlPortCannotReceiveSecretsBeforeOfflineRecovery(t *testing.T) {
	for _, wrongProtocol := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrong_identity", true: "wrong_ALPN"}[wrongProtocol], func(t *testing.T) {
			root := t.TempDir()
			identity, err := securetransport.Load(filepath.Join(root, "computer-identity"))
			if err != nil {
				t.Fatal(err)
			}
			serverIdentity := identity
			if !wrongProtocol {
				serverIdentity, err = securetransport.Load(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
			}
			protocol := "http/1.1"
			if wrongProtocol {
				protocol = "astral-computer/1"
			}
			config, err := serverIdentity.ServerConfigForProtocol(protocol)
			if err != nil {
				t.Fatal(err)
			}
			var received atomic.Int32
			fake := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				_, _ = io.ReadAll(r.Body)
				w.WriteHeader(204)
			}))
			fake.Config.ErrorLog = log.New(io.Discard, "", 0)
			fake.TLS = config
			fake.StartTLS()
			defer fake.Close()
			path := filepath.Join(root, "control.json")
			if err = WriteJSON(path, localControlInfo{URL: fake.URL, Token: strings.Repeat("1", 64), PID: 1, ServerPin: identity.Pin()}); err != nil {
				t.Fatal(err)
			}
			lock, err := Lock(filepath.Join(root, "server.lock"))
			if err != nil {
				t.Fatal(err)
			}
			err = ConfigureAdministrator(context.Background(), root, "owner", "strong-test-password", false)
			if err == nil {
				lock.Close()
				t.Fatal("failed pin/ALPN bypassed live data lock")
			}
			if received.Load() != 0 {
				lock.Close()
				t.Fatal("HTTP credentials reached a different process before TLS identity verification")
			}
			lock.Close()
			if err = ConfigureAdministrator(context.Background(), root, "owner", "strong-test-password", false); err != nil {
				t.Fatalf("safe offline recovery failed: %v", err)
			}
			if received.Load() != 0 {
				t.Fatal("offline fallback leaked secrets to stale endpoint")
			}
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("stale private control record not cleared: %v", err)
			}
		})
	}
}
func TestDisconnectedStaleControlRecoversOnlyAfterDataLock(t *testing.T) {
	root := t.TempDir()
	identity, err := securetransport.Load(filepath.Join(root, "computer-identity"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "https://" + listener.Addr().String()
	listener.Close()
	if err = WriteJSON(filepath.Join(root, "control.json"), localControlInfo{URL: url, Token: strings.Repeat("2", 64), PID: 1, ServerPin: identity.Pin()}); err != nil {
		t.Fatal(err)
	}
	if err = ConfigureAdministrator(context.Background(), root, "owner", "strong-test-password", false); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "control.json")); !os.IsNotExist(err) {
		t.Fatal("disconnected stale control was not removed")
	}
}
func TestLocalControlNeverFallsBackToPlainHTTP(t *testing.T) {
	root := t.TempDir()
	var received atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(204) }))
	defer plain.Close()
	if err := WriteJSON(filepath.Join(root, "control.json"), localControlInfo{URL: plain.URL, Token: strings.Repeat("1", 64), PID: 1, ServerPin: strings.Repeat("2", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureAdministrator(context.Background(), root, "owner", "strong-test-password", false); err == nil {
		t.Fatal("plain HTTP control record accepted")
	}
	if received.Load() != 0 {
		t.Fatal("credentials sent over plain HTTP")
	}
}
func TestNodeWebAuthFixtureUsesVerifiedPrivateHTTPS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is unavailable")
	}
	s := controlTestServer(t)
	script := `const {webFixtureCookie}=await import('./scripts/web-auth-fixture.mjs');const cookie=await webFixtureCookie(process.argv[1],process.argv[2]);if(!cookie.startsWith('astral_session='))throw new Error('missing session');`
	command := exec.Command(node, "--input-type=module", "-e", script, s.Config.DataDirectory, s.URL)
	command.Dir = filepath.Join("..", "..")
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("verified Node control fixture failed: %v\n%s", err, raw)
	}
	status, _ := s.Auth.Status(context.Background())
	if !status.Configured {
		t.Fatal("Node fixture did not create its temporary account")
	}
}
func TestNodeWebAuthFixtureRejectsWrongPinWithoutSendingCredentials(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is unavailable")
	}
	s := controlTestServer(t)
	path := filepath.Join(s.Config.DataDirectory, "control.json")
	var info localControlInfo
	if err = ReadJSON(path, &info); err != nil {
		t.Fatal(err)
	}
	info.ServerPin = strings.Repeat("0", 64)
	raw, _ := json.Marshal(info)
	script := `const {configureWebFixtureAdministrator}=await import('./scripts/web-auth-fixture.mjs');try{await configureWebFixtureAdministrator(process.argv[1],JSON.parse(process.argv[2]));throw new Error('wrong pin accepted')}catch(error){if(!error.message.includes('pin does not match'))throw error}`
	command := exec.Command(node, "--input-type=module", "-e", script, s.Config.DataDirectory, string(raw))
	command.Dir = filepath.Join("..", "..")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Node fixture did not reject wrong pin: %v\n%s", err, output)
	}
	status, _ := s.Auth.Status(context.Background())
	if status.Configured {
		t.Fatal("Node fixture sent account credentials with wrong pin")
	}
}

func TestNodeWebAuthFixtureRejectsDifferentPeerBeforeHTTPRequest(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is unavailable")
	}
	root := t.TempDir()
	trusted, err := securetransport.Load(filepath.Join(root, "computer-identity"))
	if err != nil {
		t.Fatal(err)
	}
	impostor, err := securetransport.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config, err := impostor.ServerConfigForProtocol("http/1.1")
	if err != nil {
		t.Fatal(err)
	}
	var received atomic.Int32
	fake := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(204)
	}))
	fake.Config.ErrorLog = log.New(io.Discard, "", 0)
	fake.TLS = config
	fake.StartTLS()
	defer fake.Close()
	info := localControlInfo{URL: fake.URL, Token: strings.Repeat("1", 64), PID: 1, ServerPin: trusted.Pin()}
	raw, _ := json.Marshal(info)
	script := `const {configureWebFixtureAdministrator}=await import('./scripts/web-auth-fixture.mjs');let rejected=false;try{await configureWebFixtureAdministrator(process.argv[1],JSON.parse(process.argv[2]))}catch{rejected=true}if(!rejected)throw new Error('different TLS peer accepted');`
	command := exec.Command(node, "--input-type=module", "-e", script, root, string(raw))
	command.Dir = filepath.Join("..", "..")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Node peer verification failed: %v\n%s", err, output)
	}
	if received.Load() != 0 {
		t.Fatal("Node sent private HTTP headers/body to an untrusted TLS peer")
	}
}

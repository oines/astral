package computers

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/securetransport"
)

func secureHub(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := securetransport.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(store)
	hub.SetIdentity(identity)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != TunnelPath {
			t.Error("unexpected plaintext endpoint")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Astral-Hello") != "" || r.URL.RawQuery != "" {
			t.Error("secret escaped into outer HTTP")
		}
		hub.Connect(w, r)
	}))
	t.Cleanup(func() { _ = hub.Close(); server.Close(); _ = store.Close() })
	return hub, server
}

func TestSecureEnrollmentPinConsumptionAndStatus(t *testing.T) {
	hub, server := secureHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	enrollment, err := hub.Enroll(ctx, server.URL, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enrollment.PowerShellCommand, "curl.exe") || strings.Contains(enrollment.PowerShellCommand, "Invoke-WebRequest") || !strings.Contains(enrollment.PowerShellCommand, "--proto '=https' --proto-redir '=https'") || strings.Index(enrollment.PowerShellCommand, "$LASTEXITCODE -ne 0") > strings.Index(enrollment.PowerShellCommand, "[scriptblock]::Create") {
		t.Fatal("PowerShell enrollment bootstrap is not fail-closed HTTPS")
	}
	if !strings.Contains(enrollment.Command, "--server-pin '"+hub.ServerPin()+"'") || strings.Contains(enrollment.Command, server.URL+"/install.sh") {
		t.Fatal("pairing command does not use pinned identity and trusted installer")
	}
	daemon, err := NewDaemon(t.TempDir(), Credentials{ServerURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RegisterRemote(ctx, server.URL, strings.Repeat("0", 64), enrollment.Enrollment.ID, enrollment.Token, daemon.Hello); err == nil {
		t.Fatal("accepted a different Server identity")
	}
	credentials, err := RegisterRemote(ctx, server.URL, hub.ServerPin(), enrollment.Enrollment.ID, enrollment.Token, daemon.Hello)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.ServerPin != hub.ServerPin() {
		t.Fatal("did not persist Server pin")
	}
	if _, err = RegisterRemote(ctx, server.URL, hub.ServerPin(), enrollment.Enrollment.ID, enrollment.Token, daemon.Hello); err == nil {
		t.Fatal("old registration command recovered credentials")
	}
	status, err := RemoteStatus(ctx, credentials)
	if err != nil || status.ID != credentials.ComputerID || status.TokenID != "" {
		t.Fatalf("encrypted status failed: %+v %v", status, err)
	}
	if err = hub.Revoke(ctx, credentials.ComputerID); err != nil {
		t.Fatal(err)
	}
	if _, err = RemoteStatus(ctx, credentials); err == nil {
		t.Fatal("revoked computer authenticated")
	}
}

func TestConcurrentEnrollmentConsumesTokenOnlyOnce(t *testing.T) {
	hub, server := secureHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	enrollment, err := hub.Enroll(ctx, server.URL, false, false)
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(t.TempDir(), Credentials{ServerURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var group sync.WaitGroup
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := RegisterRemote(ctx, server.URL, hub.ServerPin(), enrollment.Enrollment.ID, enrollment.Token, daemon.Hello); err == nil {
				successes.Add(1)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatalf("enrollment succeeded %d times", successes.Load())
	}
}

func TestSecureContainerPreparationAndPlaintextRejection(t *testing.T) {
	hub, server := secureHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	enrollment, err := hub.Enroll(ctx, server.URL, true, false)
	if err != nil {
		t.Fatal(err)
	}
	hub.ContainerURL = func(_ *http.Request, hostname string) string {
		if hostname != "expected" {
			t.Error("hostname lost")
		}
		return "http://host.docker.internal:4545"
	}
	if _, err = PrepareRemoteContainer(ctx, server.URL, hub.ServerPin(), enrollment.Enrollment.ID, "wrong", "expected"); err == nil {
		t.Fatal("wrong token prepared container")
	}
	address, err := PrepareRemoteContainer(ctx, server.URL, hub.ServerPin(), enrollment.Enrollment.ID, enrollment.Token, "expected")
	if err != nil || address != "http://host.docker.internal:4545" {
		t.Fatalf("prepare: %q %v", address, err)
	}
	for _, header := range []string{"Authorization", "Origin", "X-Astral-Hello"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest("GET", TunnelPath, nil)
		request.Header.Set(header, "reject")
		hub.Connect(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("accepted outer %s", header)
		}
	}
	var stored domain.Enrollment
	if err = hub.Store.Get(ctx, "enrollments", enrollment.Enrollment.ID, &stored); err != nil || stored.State != "waiting" {
		t.Fatal("preparation consumed enrollment")
	}
}

func TestAuthenticationFrameRejectsOversizeBeforeAllocation(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	go func() {
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], ^uint32(0))
		_, _ = right.Write(prefix[:])
	}()
	var request tunnelRequest
	if err := readAuthentication(left, &request); err == nil {
		t.Fatal("accepted oversized authentication")
	}
}

func TestTunnelSupportsDoubleTLSAndRetainsOuterCertificateValidation(t *testing.T) {
	hub, _ := secureHub(t)
	server := httptest.NewTLSServer(http.HandlerFunc(hub.Connect))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	enrollment, err := hub.Enroll(ctx, server.URL, false, false)
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(t.TempDir(), Credentials{ServerURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RegisterRemote(ctx, server.URL, hub.ServerPin(), enrollment.Enrollment.ID, enrollment.Token, daemon.Hello); err == nil {
		t.Fatal("inner pin bypassed the outer HTTPS certificate validation")
	}
	conn, response, err := authenticateTunnel(ctx, server.URL, hub.ServerPin(), tunnelRequest{Operation: "register", EnrollmentID: enrollment.Enrollment.ID, Token: enrollment.Token, Hello: daemon.Hello}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if response.Credentials == nil || response.Credentials.ComputerID == "" {
		t.Fatal("TLS over WSS enrollment did not complete")
	}
}

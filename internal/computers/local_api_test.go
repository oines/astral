package computers

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/transport"
)

func TestDaemonLocalAPIDerivesBindingFromEachLiveCredential(t *testing.T) {
	d, generation := securityDaemon(t, func(_ context.Context, request transport.Request, out *transport.Responder) error {
		var call GatewayRequest
		if err := json.Unmarshal(request.Params, &call); err != nil {
			return err
		}
		raw, _ := json.Marshal(call.Binding)
		if err := out.Headers(GatewayResponse{Status: http.StatusOK}); err != nil {
			return err
		}
		_, err := out.Write(raw)
		return err
	})
	a := securityBinding(t, d, "grant_a")
	b := securityBinding(t, d, "grant_b")
	_, first := securityStart(t, d, generation, a, securityToken('a'))
	_, second := securityStart(t, d, generation, b, securityToken('b'))
	defer first.finish()
	defer second.finish()
	if a.APIURL == "" || a.APIURL != b.APIURL {
		t.Fatal("bindings did not share the Daemon local API")
	}
	for _, item := range []struct {
		token   string
		binding domain.ExecutionBinding
	}{{securityToken('a'), a}, {securityToken('b'), b}} {
		request := httptest.NewRequest("GET", a.APIURL+"/v1/context", nil)
		request.Header.Set("Authorization", "Bearer "+item.token)
		response := httptest.NewRecorder()
		d.localAPI.Handler.ServeHTTP(response, request)
		var got domain.ExecutionBinding
		if json.Unmarshal(response.Body.Bytes(), &got) != nil || got != item.binding {
			t.Fatalf("token crossed command binding: %s", response.Body.String())
		}
	}
}

func TestDaemonLocalAPIPortPersistsAndOccupiedPortFails(t *testing.T) {
	root := t.TempDir()
	credentials := Credentials{ServerURL: "http://127.0.0.1:4545"}
	d, err := NewDaemon(root, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.ensureLocalAPI(); err != nil {
		t.Fatal(err)
	}
	address := d.Hello.LocalAPIURL
	if err = d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	next, err := NewDaemon(root, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if err = next.ensureLocalAPI(); err != nil {
		t.Fatal(err)
	}
	if next.Hello.LocalAPIURL != address {
		t.Fatal("Daemon changed its persisted local API port")
	}
	if err = next.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp", strings.TrimPrefix(address, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	third, err := NewDaemon(root, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close(context.Background())
	if err = third.ensureLocalAPI(); err == nil || third.Hello.LocalAPIURL != "" {
		t.Fatal("occupied saved port silently changed")
	}
}

func TestDaemonWakeSingleRPCAndFixedLocalCooldown(t *testing.T) {
	var calls atomic.Int32
	d, _ := securityDaemon(t, func(_ context.Context, request transport.Request, out *transport.Responder) error {
		calls.Add(1)
		var call WakeRequest
		if request.Method != "wake" || json.Unmarshal(request.Params, &call) != nil || call.EndpointID != "endpoint" || string(call.Body) != `{"event_id":"one","text":"hello"}` {
			t.Errorf("invalid wake RPC: %s %s", request.Method, request.Params)
		}
		if err := out.Headers(GatewayResponse{Status: http.StatusTooManyRequests, Headers: http.Header{"Retry-After": []string{"60"}}}); err != nil {
			return err
		}
		_, err := out.Write([]byte(`{"code":"rate_limited"}`))
		return err
	})
	if err := d.ensureLocalAPI(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", d.Hello.LocalAPIURL+"/hooks/endpoint", strings.NewReader(`{"event_id":"one","text":"hello"}`))
	response := httptest.NewRecorder()
	d.localAPI.Handler.ServeHTTP(response, request)
	if response.Code != 429 || calls.Load() != 1 {
		t.Fatalf("one event did not use one RPC: %d %d", response.Code, calls.Load())
	}
	d.mu.Lock()
	until := d.wakeBlocked["endpoint"]
	d.mu.Unlock()
	body := &forbiddenBody{}
	request = httptest.NewRequest("POST", d.Hello.LocalAPIURL+"/hooks/endpoint", body)
	response = httptest.NewRecorder()
	d.localAPI.Handler.ServeHTTP(response, request)
	d.mu.Lock()
	after := d.wakeBlocked["endpoint"]
	d.mu.Unlock()
	if response.Code != 429 || response.Header().Get("Retry-After") == "" || response.Header().Get("Connection") != "close" || body.reads != 0 || calls.Load() != 1 || !after.Equal(until) {
		t.Fatal("local cooldown read a body, added an RPC, or extended its deadline")
	}
	d.mu.Lock()
	d.wakeBlocked["endpoint"] = time.Now().Add(-time.Second)
	d.mu.Unlock()
	request = httptest.NewRequest("POST", d.Hello.LocalAPIURL+"/hooks/endpoint", strings.NewReader(`{"event_id":"one","text":"hello"}`))
	d.localAPI.Handler.ServeHTTP(httptest.NewRecorder(), request)
	if calls.Load() != 2 {
		t.Fatal("expired cooldown did not allow another request")
	}
}

func TestDaemonWakeBodyBoundsAndOrigin(t *testing.T) {
	var calls atomic.Int32
	d, _ := securityDaemon(t, func(context.Context, transport.Request, *transport.Responder) error { calls.Add(1); return nil })
	if err := d.ensureLocalAPI(); err != nil {
		t.Fatal(err)
	}
	for _, chunked := range []bool{false, true} {
		request := httptest.NewRequest("POST", d.Hello.LocalAPIURL+"/hooks/endpoint", nil)
		body := &forbiddenBody{}
		request.Body, request.ContentLength = body, localWakeBodyLimit+1
		if chunked {
			request.ContentLength = -1
			request.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", localWakeBodyLimit+1)))
		}
		response := httptest.NewRecorder()
		d.localAPI.Handler.ServeHTTP(response, request)
		if response.Code != 413 || body.reads != 0 || calls.Load() != 0 || response.Header().Get("Connection") != "close" {
			t.Fatalf("body bound failed: %d", response.Code)
		}
	}
	for _, change := range []func(*http.Request){func(r *http.Request) { r.Header.Set("Origin", "https://other.example") }, func(r *http.Request) { r.Host = "other.example" }} {
		body := &forbiddenBody{}
		request := httptest.NewRequest("POST", d.Hello.LocalAPIURL+"/hooks/endpoint", body)
		change(request)
		response := httptest.NewRecorder()
		d.localAPI.Handler.ServeHTTP(response, request)
		if response.Code != 403 || body.reads != 0 || calls.Load() != 0 {
			t.Fatal("origin boundary read or forwarded event")
		}
	}
	request := httptest.NewRequest("POST", d.Hello.LocalAPIURL+"/hooks/endpoint", nil)
	request.ContentLength = -1
	request.Body = failedWakeBody{}
	response := httptest.NewRecorder()
	d.localAPI.Handler.ServeHTTP(response, request)
	if response.Code != 408 || calls.Load() != 0 {
		t.Fatal("body deadline did not stop event forwarding")
	}
}

type failedWakeBody struct{}

func (failedWakeBody) Read([]byte) (int, error) { return 0, os.ErrDeadlineExceeded }
func (failedWakeBody) Close() error             { return nil }

func TestDaemonLocalAPIHelloAcceptsOnlyExplicitLoopback(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:4545", "http://127.0.0.1:1"} {
		if !validLocalAPIURL(raw) {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:0", "http://localhost:4545", "https://127.0.0.1:4545", "http://0.0.0.0:4545", "http://127.0.0.1:4545/path", "http://127.0.0.1:4545?token=x", "http://u@127.0.0.1:4545"} {
		if validLocalAPIURL(raw) {
			t.Fatal(raw)
		}
	}
}

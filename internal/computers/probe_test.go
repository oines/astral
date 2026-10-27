package computers

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/oines/astral/internal/securetransport"
)

func TestProbeServerChecksPinWithoutSendingAuthentication(t *testing.T) {
	identity, err := securetransport.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	type observation struct {
		bytes int
		err   error
	}
	observed := make(chan observation, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != TunnelPath || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Astral-Hello") != "" {
			t.Error("probe sent authentication outside the pinned TLS connection")
		}
		ws, e := websocket.Accept(w, r, nil)
		if e != nil {
			observed <- observation{err: e}
			return
		}
		raw := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
		defer raw.Close()
		conn := tls.Server(raw, identity.ServerConfig())
		handshake, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if e = conn.HandshakeContext(handshake); e != nil {
			observed <- observation{err: e}
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var first [1]byte
		n, e := conn.Read(first[:])
		observed <- observation{bytes: n, err: e}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = ProbeServer(ctx, server.URL, identity.Pin()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-observed:
		if result.bytes != 0 || !errors.Is(result.err, io.EOF) {
			t.Fatalf("probe sent application/authentication bytes or did not close: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("probe left the unauthenticated connection open")
	}
	if err = ProbeServer(ctx, server.URL, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "identity does not match") {
		t.Fatalf("probe accepted another Server identity: %v", err)
	}
}

func TestProbeServerRejectsExpiredContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ProbeServer(ctx, "http://127.0.0.1:1", strings.Repeat("0", 64))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled probe did not stop: %v", err)
	}
}

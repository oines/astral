package platform

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/securetransport"
)

// secureStatusFixture exercises the real WebSocket/TLS/status client while
// allowing updater tests to model a changing process without Docker or builds.
func secureStatusFixture(t *testing.T, status func() domain.ComputerStatus) (*httptest.Server, computers.Credentials) {
	t.Helper()
	identity, err := securetransport.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != computers.TunnelPath || r.Header.Get("Authorization") != "" {
			t.Error("status escaped encrypted tunnel")
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		raw := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
		ws.SetReadLimit(64 << 10)
		defer raw.Close()
		conn := tls.Server(raw, identity.ServerConfig())
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err = conn.HandshakeContext(ctx); err != nil {
			return
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		var prefix [4]byte
		if _, err = io.ReadFull(conn, prefix[:]); err != nil {
			return
		}
		size := binary.BigEndian.Uint32(prefix[:])
		if size == 0 || size > 16<<10 {
			t.Error("invalid authentication size")
			return
		}
		data := make([]byte, int(size))
		if _, err = io.ReadFull(conn, data); err != nil {
			return
		}
		var request struct {
			Operation  string `json:"operation"`
			Token      string `json:"token"`
			ComputerID string `json:"computer_id"`
		}
		if err = json.Unmarshal(data, &request); err != nil {
			t.Error(err)
			return
		}
		if request.Operation != "status" || request.Token != "enrolled-token" || request.ComputerID != "computer-id" {
			t.Error("invalid encrypted status credential")
			return
		}
		data, err = json.Marshal(map[string]any{"status": status()})
		if err != nil {
			t.Error(err)
			return
		}
		binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
		_, _ = conn.Write(prefix[:])
		_, _ = conn.Write(data)
	}))
	t.Cleanup(server.Close)
	return server, computers.Credentials{ServerURL: server.URL, ServerPin: identity.Pin(), ComputerID: "computer-id", Token: "enrolled-token"}
}

package computers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/securetransport"
	"github.com/oines/astral/internal/serviceurl"
)

const TunnelPath = "/internal/computer-tunnel"
const authenticationLimit = 16 << 10

type tunnelRequest struct {
	Operation    string `json:"operation"`
	EnrollmentID string `json:"enrollment_id,omitempty"`
	ComputerID   string `json:"computer_id,omitempty"`
	Token        string `json:"token"`
	Hello        Hello  `json:"hello,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
}
type tunnelResponse struct {
	Credentials       *Credentials           `json:"credentials,omitempty"`
	Status            *domain.ComputerStatus `json:"status,omitempty"`
	LocalContainerURL string                 `json:"local_container_url,omitempty"`
	Error             string                 `json:"error,omitempty"`
}

// These bounded frames are used only for the initial authentication exchange.
// The authenticated connection then carries transport.Peer's bounded frames.
func writeAuthentication(conn net.Conn, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > authenticationLimit {
		return errors.New("computer authentication message is too large")
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(raw)))
	_, err = io.Copy(conn, io.MultiReader(bytes.NewReader(prefix[:]), bytes.NewReader(raw)))
	return err
}
func readAuthentication(conn net.Conn, value any) error {
	var prefix [4]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 || size > authenticationLimit {
		return errors.New("invalid computer authentication message size")
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(conn, raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}

func dialTunnel(ctx context.Context, serverURL, pin string, client *http.Client) (*tls.Conn, error) {
	if err := serviceurl.Validate(serverURL); err != nil {
		return nil, err
	}
	config, err := securetransport.ClientConfig(pin)
	if err != nil {
		return nil, err
	}
	u := strings.TrimRight(serverURL, "/") + TunnelPath
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ws, response, err := websocket.Dial(dialCtx, u, &websocket.DialOptions{HTTPClient: client})
	cancel()
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("computer tunnel rejected (HTTP %d)", response.StatusCode)
		}
		return nil, fmt.Errorf("computer cannot reach Server: %w", err)
	}
	raw := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	// NetConn disables its read limit. TLS records are small; reapply a bound
	// so an attacker cannot place unbounded data in a single WebSocket message.
	ws.SetReadLimit(64 << 10)
	conn := tls.Client(raw, config)
	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, 10*time.Second)
	err = conn.HandshakeContext(handshakeCtx)
	handshakeCancel()
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("secure computer handshake failed: %w", err)
	}
	return conn, nil
}
func authenticateTunnel(ctx context.Context, serverURL, pin string, request tunnelRequest, client *http.Client) (*tls.Conn, tunnelResponse, error) {
	conn, err := dialTunnel(ctx, serverURL, pin, client)
	if err != nil {
		return nil, tunnelResponse{}, err
	}
	deadline := time.Now().Add(15 * time.Second)
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	_ = conn.SetDeadline(deadline)
	var response tunnelResponse
	err = writeAuthentication(conn, request)
	if err == nil {
		err = readAuthentication(conn, &response)
	}
	if err == nil && response.Error != "" {
		err = errors.New(response.Error)
	}
	if err != nil {
		_ = conn.Close()
		return nil, response, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, response, nil
}

func RegisterRemote(ctx context.Context, serverURL, pin, id, token string, hello Hello) (Credentials, error) {
	client := NewHTTPClient()
	defer client.CloseIdleConnections()
	conn, response, err := authenticateTunnel(ctx, serverURL, pin, tunnelRequest{Operation: "register", EnrollmentID: id, Token: token, Hello: hello}, client)
	if err != nil {
		return Credentials{}, err
	}
	defer conn.Close()
	if response.Credentials == nil || response.Credentials.ComputerID == "" || response.Credentials.Token == "" {
		return Credentials{}, errors.New("Server returned incomplete computer credentials")
	}
	credentials := *response.Credentials
	credentials.ServerURL, credentials.ServerPin = serverURL, pin
	return credentials, nil
}
func PrepareRemoteContainer(ctx context.Context, serverURL, pin, id, token, hostname string) (string, error) {
	client := NewHTTPClient()
	defer client.CloseIdleConnections()
	conn, response, err := authenticateTunnel(ctx, serverURL, pin, tunnelRequest{Operation: "prepare", EnrollmentID: id, Token: token, Hostname: hostname}, client)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return response.LocalContainerURL, nil
}
func RemoteStatus(ctx context.Context, credentials Credentials) (domain.ComputerStatus, error) {
	client := NewHTTPClient()
	defer client.CloseIdleConnections()
	conn, response, err := authenticateTunnel(ctx, credentials.ServerURL, credentials.ServerPin, tunnelRequest{Operation: "status", ComputerID: credentials.ComputerID, Token: credentials.Token}, client)
	if err != nil {
		return domain.ComputerStatus{}, err
	}
	defer conn.Close()
	if response.Status == nil {
		return domain.ComputerStatus{}, errors.New("Server returned no computer status")
	}
	return *response.Status, nil
}

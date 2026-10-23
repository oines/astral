package platform

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
)

type containerAccessResponse struct {
	ServerURL string `json:"server_url"`
	ServerCA  string `json:"server_ca"`
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}

// containerAccess exchanges an existing enrollment's proof for its same-host
// Docker connection address. This route belongs only to the primary listener.
func (s *Server) containerAccess(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !isLoopbackHost(host) {
		http.Error(w, "local container access requires a loopback connection", http.StatusForbidden)
		return
	}
	var request struct {
		Hostname string `json:"hostname"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request) != nil {
		http.Error(w, "invalid container access request", 400)
		return
	}
	local, _ := os.Hostname()
	if local == "" || request.Hostname != local {
		http.Error(w, "the Server belongs to another host; use its container-reachable address", 409)
		return
	}
	var enrollment domain.Enrollment
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	hash := sha256.Sum256([]byte(token))
	if s.Store.Get(r.Context(), "enrollments", r.PathValue("id"), &enrollment) != nil || !enrollment.Container || time.Now().After(enrollment.ExpiresAt) || token == "" || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(hash[:])), []byte(enrollment.TokenHash)) != 1 {
		http.Error(w, "invalid or expired container enrollment", 403)
		return
	}
	address, ca, err := s.ensureContainerBridge(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(containerAccessResponse{ServerURL: address, ServerCA: ca})
}

// containerBridge is a separately authenticated TLS entry point for local
// Docker Desktop containers. The public listener never serves Owner APIs,
// installers, downloads, or Web UI. Its pinned identity and port survive Server
// restarts, so already-enrolled containers can reconnect without recreation.
type containerBridge struct {
	server   *http.Server
	listener net.Listener
	state    containerBridgeState
}
type containerBridgeState struct {
	Port        int    `json:"port"`
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
}

func (s *Server) ensureContainerBridge(ctx context.Context) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", "", errors.New("Server is shutting down")
	}
	if s.containerBridge != nil {
		return bridgeAddress(s.containerBridge.state), s.containerBridge.state.Certificate, nil
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	path := filepath.Join(s.Config.DataDirectory, "container-bridge.json")
	var state containerBridgeState
	err := ReadJSON(path, &state)
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return "", "", fmt.Errorf("read local container TLS bridge: %w", err)
	}
	if fresh {
		state, err = newContainerBridgeIdentity()
		if err != nil {
			return "", "", err
		}
	}
	if state.Port < 0 || state.Port > 65535 || (!fresh && state.Port == 0) {
		return "", "", errors.New("invalid persisted container bridge port")
	}
	certificate, err := tls.X509KeyPair([]byte(state.Certificate), []byte(state.PrivateKey))
	if err != nil {
		return "", "", fmt.Errorf("invalid persisted container bridge identity: %w", err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(state.Port)))
	if err != nil {
		return "", "", fmt.Errorf("listen for local Docker containers: %w", err)
	}
	state.Port = listener.Addr().(*net.TCPAddr).Port
	if fresh {
		if err = WriteJSON(path, state); err != nil {
			_ = listener.Close()
			return "", "", err
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/enrollments/{id}", s.register)
	mux.HandleFunc("GET /internal/computers/{id}/connect", s.Hub.Connect)
	mux.HandleFunc("GET /internal/computers/{id}/status", s.computerStatus)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}}
	bridge := &containerBridge{server: server, listener: listener, state: state}
	s.containerBridge = bridge
	s.workers.Add(1)
	go func() { defer s.workers.Done(); _ = server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
	return bridgeAddress(state), state.Certificate, nil
}

// restoreContainerBridge is called during Server startup. It only opens a
// bridge when a previous local container creation has persisted one.
func (s *Server) restoreContainerBridge(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(s.Config.DataDirectory, "container-bridge.json")); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	_, _, err := s.ensureContainerBridge(ctx)
	return err
}
func (s *Server) closeContainerBridge(ctx context.Context) {
	s.mu.Lock()
	bridge := s.containerBridge
	s.containerBridge = nil
	s.mu.Unlock()
	if bridge != nil {
		if err := bridge.server.Shutdown(ctx); err != nil {
			_ = bridge.server.Close()
		}
	}
}
func bridgeAddress(state containerBridgeState) string {
	return "https://" + net.JoinHostPort("host.docker.internal", strconv.Itoa(state.Port))
}
func newContainerBridgeIdentity() (containerBridgeState, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return containerBridgeState{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return containerBridgeState{}, err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Astral local container bridge"}, DNSNames: []string{"host.docker.internal", "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return containerBridgeState{}, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return containerBridgeState{}, err
	}
	return containerBridgeState{Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))}, nil
}

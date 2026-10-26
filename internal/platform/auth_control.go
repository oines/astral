package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/securetransport"
)

type localControlInfo struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	PID       int    `json:"pid"`
	ServerPin string `json:"server_pin"`
}
type localControl struct {
	server   *http.Server
	listener net.Listener
	path     string
	done     chan struct{}
}

func RuntimeToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// This machine-local administrative channel is protected by the data directory's
// OS permissions and a per-process secret. It never grants a Web/Desktop session.
func (s *Server) startLocalControl() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	token, err := RuntimeToken()
	if err != nil {
		listener.Close()
		return err
	}
	identity, err := securetransport.Load(filepath.Join(s.Config.DataDirectory, "computer-identity"))
	if err != nil {
		listener.Close()
		return err
	}
	tlsConfig, err := identity.ServerConfigForProtocol("http/1.1")
	if err != nil {
		listener.Close()
		return err
	}
	listener = tls.NewListener(listener, tlsConfig)
	path := filepath.Join(s.Config.DataDirectory, "control.json")
	control := &localControl{listener: listener, path: path, done: make(chan struct{})}
	control.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 4096}
	control.server.BaseContext = func(net.Listener) context.Context { return s.ctx }
	control.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !loopbackRequest(r.RemoteAddr) || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("LocalControl "+token)) != 1 {
			http.Error(w, "local administrative authorization required", http.StatusForbidden)
			return
		}
		if r.Method != "POST" || (r.URL.Path != "/setup" && r.URL.Path != "/reset") {
			http.NotFound(w, r)
			return
		}
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, "invalid account input", http.StatusBadRequest)
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid account input", http.StatusBadRequest)
			return
		}
		var err error
		if r.URL.Path == "/setup" {
			err = s.Auth.Setup(r.Context(), input.Username, input.Password)
		} else {
			err = s.Auth.Reset(r.Context(), input.Username, input.Password)
		}
		if err != nil {
			status := http.StatusBadRequest
			var problem *domain.Error
			if errors.As(err, &problem) && problem.Status != 0 {
				status = problem.Status
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err = WriteJSON(path, localControlInfo{URL: "https://" + listener.Addr().String(), Token: token, PID: os.Getpid(), ServerPin: identity.Pin()}); err != nil {
		listener.Close()
		return err
	}
	s.control = control
	go func() { defer close(control.done); _ = control.server.Serve(listener) }()
	return nil
}
func (c *localControl) Close(ctx context.Context) error {
	err := c.server.Shutdown(ctx)
	_ = c.listener.Close()
	select {
	case <-c.done:
	case <-ctx.Done():
		err = errors.Join(err, ctx.Err())
	}
	if removeErr := os.Remove(c.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		err = errors.Join(err, removeErr)
	}
	return err
}

// ConfigureAdministrator works while the Server is running, including docker
// exec, or offline while exclusively holding the Server's ordinary data lock.
func ConfigureAdministrator(ctx context.Context, directory, username, password string, reset bool) error {
	var info localControlInfo
	err := ReadJSON(filepath.Join(directory, "control.json"), &info)
	if err == nil {
		parsed, parseErr := url.Parse(info.URL)
		if parseErr != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || !loopbackRequest(parsed.Host) || len(info.Token) != 64 {
			return fmt.Errorf("invalid local administrative endpoint")
		}
		pinnedTLS, pinErr := securetransport.ClientConfigForProtocol(info.ServerPin, "http/1.1")
		if pinErr != nil {
			return fmt.Errorf("invalid local administrative identity: %w", pinErr)
		}
		raw, _ := json.Marshal(map[string]string{"username": username, "password": password})
		operation := "/setup"
		if reset {
			operation = "/reset"
		}
		request, err := http.NewRequestWithContext(ctx, "POST", info.URL+operation, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "LocalControl "+info.Token)
		request.Header.Set("Content-Type", "application/json")
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.TLSClientConfig = pinnedTLS
		transport.ForceAttemptHTTP2 = false
		transport.TLSHandshakeTimeout = 5 * time.Second
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A stale port may now belong to another local process. TLS pin
			// verification rejected it before sending the password/token. Only
			// exclusive ownership of the Server data permits offline recovery.
			return configureAdministratorOffline(ctx, directory, username, password, reset, err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			return fmt.Errorf("account configuration failed: %s", bytes.TrimSpace(body))
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return configureAdministratorOffline(ctx, directory, username, password, reset, nil)
}
func configureAdministratorOffline(ctx context.Context, directory, username, password string, reset bool, connectionErr error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := Lock(filepath.Join(directory, "server.lock"))
	if err != nil {
		if connectionErr != nil {
			return fmt.Errorf("local administrative channel is unavailable and the Server data is still in use: %w", errors.Join(connectionErr, err))
		}
		return err
	}
	defer lock.Close()
	// A live Server cannot replace this file while its data lock is ours.
	if err = os.Remove(filepath.Join(directory, "control.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	store, err := persistence.Open(directory)
	if err != nil {
		return err
	}
	defer store.Close()
	service, err := auth.New(store, nil)
	if err != nil {
		return err
	}
	if reset {
		return service.Reset(ctx, username, password)
	}
	return service.Setup(ctx, username, password)
}

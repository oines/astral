package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpapi"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/securetransport"
)

// These routes exist only on the pinned, OS-user-owned administrative channel.
// They are deliberately absent from the Web listener and its Host policy.
func (s *Server) localConfiguration(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	var output any
	var err error
	switch r.URL.Path {
	case "/status":
		output, err = s.Auth.Status(r.Context())
	case "/hostnames":
		output = s.network.snapshot()
	case "/hostnames/disable":
		disabled := false
		output, err = s.SaveNetworkSettings(r.Context(), httpapi.NetworkPatch{RestrictHostnames: &disabled})
	default:
		return false
	}
	if err != nil {
		status := http.StatusBadRequest
		var problem *domain.Error
		if errors.As(err, &problem) && problem.Status != 0 {
			status = problem.Status
		}
		http.Error(w, err.Error(), status)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(output)
	return true
}

// requestLocalConfiguration returns handled=false only when there is no live
// control endpoint. Offline fallback must still acquire the ordinary Server
// data lock; a wrong/stale TLS endpoint never receives the private token.
func requestLocalConfiguration(ctx context.Context, directory, operation string, output any) (handled bool, err error) {
	var info localControlInfo
	if err = ReadJSON(filepath.Join(directory, "control.json"), &info); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return true, err
	}
	parsed, err := url.Parse(info.URL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || !loopbackRequest(parsed.Host) || len(info.Token) != 64 {
		return true, fmt.Errorf("invalid local administrative endpoint")
	}
	pinnedTLS, err := securetransport.ClientConfigForProtocol(info.ServerPin, "http/1.1")
	if err != nil {
		return true, fmt.Errorf("invalid local administrative identity: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, info.URL+operation, nil)
	if err != nil {
		return true, err
	}
	request.Header.Set("Authorization", "LocalControl "+info.Token)
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
			return true, ctx.Err()
		}
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return true, fmt.Errorf("local configuration failed: %s", bytes.TrimSpace(body))
	}
	return true, json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(output)
}

func localDataLock(directory string, connectionErr error) (*os.File, error) {
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("no Server data directory at %s", directory)
	}
	lock, err := Lock(filepath.Join(directory, "server.lock"))
	if err != nil && connectionErr != nil {
		return nil, fmt.Errorf("local administrative channel is unavailable and the Server data is still in use: %w", errors.Join(connectionErr, err))
	}
	return lock, err
}

// AdministratorStatus can inspect an installed Server without creating an
// account, signing in to Web, or writing a new database at a guessed location.
func AdministratorStatus(ctx context.Context, directory string) (auth.Status, error) {
	var status auth.Status
	if handled, err := requestLocalConfiguration(ctx, directory, "/status", &status); handled {
		return status, err
	} else {
		lock, lockErr := localDataLock(directory, err)
		if lockErr != nil {
			return status, lockErr
		}
		defer lock.Close()
	}
	if _, err := os.Stat(filepath.Join(directory, "astral.db")); errors.Is(err, os.ErrNotExist) {
		return status, nil
	} else if err != nil {
		return status, err
	}
	store, err := persistence.Open(directory)
	if err != nil {
		return status, err
	}
	defer store.Close()
	service, err := auth.New(store, nil)
	if err != nil {
		return status, err
	}
	return service.Status(ctx)
}

// LocalHostnameSettings reads or disables the hostname policy without relying
// on the public Web entry. Existing list contents are retained when disabled.
func LocalHostnameSettings(ctx context.Context, directory string, disable bool) (httpapi.NetworkSettings, error) {
	var settings httpapi.NetworkSettings
	operation := "/hostnames"
	if disable {
		operation = "/hostnames/disable"
	}
	handled, connectionErr := requestLocalConfiguration(ctx, directory, operation, &settings)
	if handled {
		return settings, connectionErr
	}
	lock, err := localDataLock(directory, connectionErr)
	if err != nil {
		return settings, err
	}
	defer lock.Close()
	if err = ctx.Err(); err != nil {
		return settings, err
	}
	if _, err = os.Stat(filepath.Join(directory, "astral.db")); err != nil {
		return settings, fmt.Errorf("no existing Server database at %s: %w", directory, err)
	}
	// Inspecting an offline account must not choose standalone defaults before
	// the first Server starts, which may be managed by Desktop instead.
	network, err := loadNetworkState(Config{DataDirectory: directory}, false)
	if err != nil {
		return settings, err
	}
	if !disable {
		return network.snapshot(), nil
	}
	value := false
	server := Server{network: network}
	return server.SaveNetworkSettings(ctx, httpapi.NetworkPatch{RestrictHostnames: &value})
}

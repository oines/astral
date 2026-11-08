package computers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/transport"
)

const localWakeBodyLimit = 64 << 10
const localWakeReadTimeout = 10 * time.Second
const localWakeBlockLimit = 256

func validLocalAPIURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535
}

// ComputerEventURL returns the actual local entry point advertised by the live
// owning Daemon. It never substitutes the Server or another Computer's address.
func (h *Hub) ComputerEventURL(id string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if active := h.live[id]; active != nil {
		return active.hello.LocalAPIURL
	}
	return ""
}

func (d *Daemon) ensureLocalAPI() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.startLocalAPILocked()
}

func (d *Daemon) startLocalAPILocked() error {
	if d.closed {
		return domain.Offline("daemon is closed")
	}
	if d.localAPI != nil {
		return nil
	}
	path := filepath.Join(d.Root, "local-api.json")
	config := struct {
		Address string `json:"address"`
	}{Address: "127.0.0.1:0"}
	raw, err := os.ReadFile(path)
	saved := err == nil
	if saved {
		if json.Unmarshal(raw, &config) != nil || !validLocalAPIURL("http://"+config.Address) {
			return fmt.Errorf("invalid local API address in %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("tcp", config.Address)
	if err != nil {
		return fmt.Errorf("listen on saved local API address %s: %w", config.Address, err)
	}
	if !saved {
		config.Address = listener.Addr().String()
		raw, err = json.Marshal(config)
		if err == nil {
			var temp *os.File
			temp, err = os.CreateTemp(d.Root, ".local-api-*")
			if err == nil {
				name := temp.Name()
				defer os.Remove(name)
				_, err = temp.Write(raw)
				if closeErr := temp.Close(); err == nil {
					err = closeErr
				}
				if err == nil {
					err = os.Rename(name, path)
				}
			}
		}
		if err != nil {
			_ = listener.Close()
			return err
		}
	}
	d.Hello.LocalAPIURL = "http://" + listener.Addr().String()
	d.wakeSlots = make(chan struct{}, 8)
	d.wakeBlocked = map[string]time.Time{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/{id}", d.receiveWake)
	mux.HandleFunc("/v1/", d.receiveCommandAPI)
	address := listener.Addr().String()
	d.localAPI = &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" || r.Host != address {
			w.Header().Set("Connection", "close")
			http.Error(w, "invalid local gateway origin", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})}
	server := d.localAPI
	d.localListener = listener
	go func() { _ = server.Serve(listener) }()
	return nil
}

func (d *Daemon) receiveCommandAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		http.Error(w, "unsupported gateway operation", http.StatusNotFound)
		return
	}
	apiToken, binding, peer := d.localAPICredential(r)
	if peer == nil {
		w.Header().Set("Connection", "close")
		rejectLocalAPI(w)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 48<<10))
	if err != nil || len(raw) > 0 && !json.Valid(raw) {
		w.Header().Set("Connection", "close")
		http.Error(w, "gateway expects a small JSON body", http.StatusBadRequest)
		return
	}
	d.forwardLocal(w, r, peer, "gateway", GatewayRequest{Binding: binding, APIToken: apiToken, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Range: r.Header.Get("Range"), Body: raw, Intent: r.Header.Get("Idempotency-Key")}, "")
}

func localWakeFailure(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

func (d *Daemon) receiveWake(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(localWakeReadTimeout))
	bodyRead := false
	defer func() {
		if bodyRead {
			_ = controller.SetReadDeadline(time.Time{})
		}
	}()
	reject := func(status int, code, message string) {
		if !bodyRead {
			w.Header().Set("Connection", "close")
			_ = controller.SetReadDeadline(time.Now())
		}
		localWakeFailure(w, status, code, message)
	}
	id := r.PathValue("id")
	if r.URL.RawQuery != "" {
		reject(http.StatusBadRequest, "invalid", "Event endpoints do not accept query parameters.")
		return
	}
	now := time.Now()
	d.mu.Lock()
	until := d.wakeBlocked[id]
	if !until.After(now) {
		delete(d.wakeBlocked, id)
	}
	var peer *transport.Peer
	if d.connectedLocked(d.connection) {
		peer = d.connection.peer
	}
	d.mu.Unlock()
	if until.After(now) {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(until.Sub(now).Seconds())))))
		reject(http.StatusTooManyRequests, "rate_limited", "This endpoint is temporarily paused after too many events.")
		return
	}
	if peer == nil {
		reject(http.StatusServiceUnavailable, "offline", "The Daemon is not connected to its Server.")
		return
	}
	select {
	case d.wakeSlots <- struct{}{}:
		defer func() { <-d.wakeSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		reject(http.StatusTooManyRequests, "rate_limited", "Too many event requests. Try again shortly.")
		return
	}
	if r.ContentLength > localWakeBodyLimit {
		reject(http.StatusRequestEntityTooLarge, "request_too_large", "Event requests must not exceed 64 KiB.")
		return
	}
	limited := http.MaxBytesReader(w, r.Body, localWakeBodyLimit)
	raw, err := io.ReadAll(limited)
	if err != nil {
		var oversized *http.MaxBytesError
		var networkError net.Error
		switch {
		case errors.As(err, &oversized):
			reject(http.StatusRequestEntityTooLarge, "request_too_large", "Event requests must not exceed 64 KiB.")
		case errors.Is(err, os.ErrDeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout():
			reject(http.StatusRequestTimeout, "request_timeout", "The event request body took too long to arrive.")
		default:
			reject(http.StatusBadRequest, "invalid", "The event request body could not be read.")
		}
		_ = limited.Close()
		return
	}
	_ = limited.Close()
	bodyRead = true
	if len(raw) == 0 || !json.Valid(raw) || r.Header.Get("Content-Type") != "" && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		reject(http.StatusBadRequest, "invalid", "Event endpoints expect a JSON object.")
		return
	}
	d.forwardLocal(w, r, peer, "wake", WakeRequest{EndpointID: id, Body: raw}, id)
}

func (d *Daemon) cacheWakeLimit(id string, response GatewayResponse) {
	if id == "" || response.Status != http.StatusTooManyRequests {
		return
	}
	seconds, err := strconv.Atoi(response.Headers.Get("Retry-After"))
	if err != nil || seconds <= 0 {
		return
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, until := range d.wakeBlocked {
		if !until.After(now) {
			delete(d.wakeBlocked, key)
		}
	}
	if previous := d.wakeBlocked[id]; !previous.IsZero() && previous.After(now) {
		return
	}
	if len(d.wakeBlocked) >= localWakeBlockLimit {
		var oldestID string
		var oldest time.Time
		for key, until := range d.wakeBlocked {
			if oldestID == "" || until.Before(oldest) {
				oldestID, oldest = key, until
			}
		}
		delete(d.wakeBlocked, oldestID)
	}
	until := now.Add(time.Duration(min(seconds, 60)) * time.Second)
	d.wakeBlocked[id] = until
}

func (d *Daemon) forwardLocal(w http.ResponseWriter, r *http.Request, peer *transport.Peer, method string, call any, wakeID string) {
	var response GatewayResponse
	meta, body, err := peer.Call(r.Context(), method, call, nil)
	if err == nil {
		err = json.Unmarshal(meta, &response)
	}
	if body != nil {
		defer body.Close()
	}
	if err != nil {
		status, code := http.StatusBadGateway, "outcome_unknown"
		var known *transport.Error
		if errors.As(err, &known) && known.Outcome == "not_executed" {
			status, code = http.StatusForbidden, known.Code
			if known.Code == "unauthorized" {
				status = http.StatusUnauthorized
				w.Header().Set("WWW-Authenticate", "Bearer")
			}
		}
		localWakeFailure(w, status, code, err.Error())
		return
	}
	d.cacheWakeLimit(wakeID, response)
	for key, values := range response.Headers {
		w.Header()[key] = values
	}
	if response.Status < 100 || response.Status > 599 {
		response.Status = http.StatusBadGateway
	}
	w.WriteHeader(response.Status)
	if body != nil {
		if _, err := io.Copy(w, body); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

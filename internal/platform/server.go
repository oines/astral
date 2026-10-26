package platform

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/httpapi"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/securetransport"
)

// The release build copies the shared Vite output into web/. A source checkout
// can instead pass --web-dir apps/web/dist.
//
//go:embed web
var embeddedWeb embed.FS

type Config struct {
	Listen                string
	PublicURLs            []string
	DataDirectory         string
	ComputerDirectory     string
	WebDirectory          string
	DistributionDirectory string
	Executable            string
	Version               string
	AllowRemoteManagement bool
	NetworkCLI            NetworkValues
	NetworkEnvironment    NetworkValues
	DesktopToken          string
	InitialAdminUsername  string
	InitialAdminPassword  string
	SessionLifetime       time.Duration
	TrustedProxies        []string
	NoLocal               bool
}
type Running struct {
	PID       int       `json:"pid"`
	URL       string    `json:"url"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
}
type Server struct {
	Config           Config
	Store            *persistence.Store
	CoreStore        *sqlite.Store
	Hub              *computers.Hub
	Harness          *harness.Service
	Channels         *channels.Service
	Owner            *httpapi.API
	Listener         net.Listener
	HTTP             *http.Server
	URL              string
	Auth             *auth.Service
	control          *localControl
	lock             *os.File
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex
	local            *computers.Daemon
	localLock        *os.File
	localCancel      context.CancelFunc
	localDone        chan struct{}
	workers          sync.WaitGroup
	provisioning     map[string]bool
	closed           bool
	closeDone        chan struct{}
	startupError     string
	network          *networkState
	loopbackListener net.Listener
}

func New(config Config) (server *Server, err error) {
	root, err := DefaultRoot()
	if err != nil {
		return nil, err
	}
	if config.DataDirectory == "" {
		config.DataDirectory = filepath.Join(root, "server")
	}
	if config.ComputerDirectory == "" {
		config.ComputerDirectory = filepath.Join(root, "computers")
	}
	if config.Version == "" {
		config.Version = "dev"
	}
	if config.Executable == "" {
		config.Executable, _ = os.Executable()
	}
	config.DataDirectory, err = filepath.Abs(config.DataDirectory)
	if err != nil {
		return nil, err
	}
	config.ComputerDirectory, err = filepath.Abs(config.ComputerDirectory)
	if err != nil {
		return nil, err
	}
	s := &Server{Config: config, provisioning: map[string]bool{}, closeDone: make(chan struct{})}
	defer func() {
		if err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.Close(ctx)
		}
	}()
	s.lock, err = Lock(filepath.Join(config.DataDirectory, "server.lock"))
	if err != nil {
		return nil, err
	}
	s.network, err = newNetworkState(config)
	if err != nil {
		return nil, err
	}
	s.Config.Listen = s.network.activeListen.Value
	s.Store, err = persistence.Open(config.DataDirectory)
	if err != nil {
		return nil, err
	}
	if err = s.restoreContainerMetadata(context.Background()); err != nil {
		return nil, err
	}
	s.CoreStore, err = sqlite.Open(filepath.Join(config.DataDirectory, "core.db"))
	if err != nil {
		return nil, err
	}
	s.Listener, err = net.Listen("tcp", s.Config.Listen)
	if err != nil {
		return nil, err
	}
	address := s.Listener.Addr().String()
	host, port, _ := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	} else if ip == nil || !ip.IsLoopback() {
		// Desktop and the managed local Daemon always retain a loopback
		// management connection, even when the main listener binds one NIC.
		s.loopbackListener, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			s.loopbackListener, err = net.Listen("tcp", "127.0.0.1:0")
		}
		if err != nil {
			return nil, err
		}
		host, port, _ = net.SplitHostPort(s.loopbackListener.Addr().String())
	}
	s.URL = "http://" + net.JoinHostPort(host, port)
	discoveryCtx, stopDiscovery := context.WithTimeout(context.Background(), 2*time.Second)
	s.network.addresses = s.network.discover(discoveryCtx, address, s.URL)
	s.network.discoveredAt = time.Now()
	stopDiscovery()
	s.Config.PublicURLs = s.network.snapshot().PublicURLs.Value

	s.ctx, s.cancel = context.WithCancel(context.Background())
	identity, identityErr := securetransport.Load(filepath.Join(config.DataDirectory, "computer-identity"))
	if identityErr != nil {
		return nil, identityErr
	}
	s.Hub = computers.NewHub(s.Store)
	s.Hub.SetIdentity(identity)
	s.Hub.ContainerURL = func(r *http.Request, hostname string) string {
		local, _ := os.Hostname()
		if hostname != "" && local == hostname && loopbackRequest(r.RemoteAddr) {
			return s.ContainerURL()
		}
		return ""
	}
	s.Auth, err = auth.New(s.Store, &auth.Config{DesktopToken: config.DesktopToken, WebOrigins: s.WebOrigins, RequestOrigin: s.RequestOrigin, SessionLifetime: config.SessionLifetime})
	if err != nil {
		return nil, err
	}
	if config.InitialAdminUsername != "" || config.InitialAdminPassword != "" {
		status, statusErr := s.Auth.Status(context.Background())
		if statusErr != nil {
			return nil, statusErr
		}
		if !status.Configured {
			if err = s.Auth.Setup(context.Background(), config.InitialAdminUsername, config.InitialAdminPassword); err != nil {
				return nil, err
			}
		}
	}
	s.Config.InitialAdminPassword = ""

	s.Harness = harness.New(s.Store, s.CoreStore, &modelbridge.Bridge{Store: s.Store, ModelCatalog: modelbridge.NewModelCatalog(filepath.Join(s.Store.Directory, "cache", "models.dev.json"), nil)}, s.Hub)
	// Deletion recovery can remove IM accounts before any channel worker starts.
	s.Channels = channels.New(s.Store, s.Harness)
	if err = s.Harness.ResumeSessionDeletions(s.ctx); err != nil {
		return nil, err
	}
	if err = s.Harness.InitializeOnboarding(s.ctx); err != nil {
		return nil, err
	}
	agentGateway := gateway.New(s.Harness, s.Channels)
	s.Hub.Gateway = agentGateway.Handle
	s.Owner = httpapi.New(httpapi.Config{Harness: s.Harness, Channels: s.Channels, Auth: s.Auth, PublicURL: s.PublicURL(), DataDirectory: config.DataDirectory, Version: config.Version,
		HostInfo: s.HostInfo, UseLocal: s.UseLocal, CreateContainer: s.CreateContainer,
		NetworkSettings: s.NetworkSettings, SaveNetworkSettings: s.SaveNetworkSettings, ValidateAccessAddresses: s.ValidateAccessAddresses})
	mux := http.NewServeMux()
	mux.Handle("/api/", s.Owner)
	mux.Handle("/auth/", s.Owner.AuthHandler())
	mux.HandleFunc("/internal/", http.NotFound)
	mux.HandleFunc("/downloads/", http.NotFound)
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) { httpcontract.JSON(w, 200, httpapi.Contract().Document()) })
	computerMux := http.NewServeMux()
	for _, routes := range []*http.ServeMux{mux, computerMux} {
		routes.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
			httpcontract.JSON(w, 200, map[string]string{"status": "ok", "version": s.Config.Version})
		})
		routes.HandleFunc("GET "+computers.TunnelPath, s.Hub.Connect)
	}
	mux.Handle("/", s.web())
	s.HTTP = &http.Server{Handler: s.accessHandler(s.managementBoundary(mux), computerMux), BaseContext: func(net.Listener) context.Context { return s.ctx }, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	return s, nil
}
func (s *Server) Start() error {
	if err := s.startLocalControl(); err != nil {
		return err
	}

	if err := s.Channels.Start(s.ctx); err != nil {
		return err
	}
	listeners := []net.Listener{s.Listener}
	if s.loopbackListener != nil {
		listeners = append(listeners, s.loopbackListener)
	}
	for _, listener := range listeners {
		s.workers.Add(1)
		go func() { defer s.workers.Done(); _ = s.HTTP.Serve(listener) }()
	}

	if err := WriteJSON(filepath.Join(s.Config.DataDirectory, "running.json"), Running{PID: os.Getpid(), URL: s.URL, Version: s.Config.Version, StartedAt: time.Now().UTC()}); err != nil {
		return err
	}
	if !s.Config.NoLocal {
		ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
		defer cancel()
		if _, err := s.useLocal(ctx, false); err != nil {
			s.mu.Lock()
			s.startupError = err.Error()
			s.mu.Unlock()
		}
	}
	return nil
}
func (s *Server) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		select {
		case <-s.closeDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.closed = true
	s.mu.Unlock()
	defer close(s.closeDone)
	if s.cancel != nil {
		s.cancel()
	}
	var all []error
	if s.control != nil {
		all = append(all, s.control.Close(ctx))
	}
	if s.Channels != nil {
		all = append(all, s.Channels.Close(ctx))
	}
	if s.Harness != nil {
		all = append(all, s.Harness.Close(ctx))
	}
	s.mu.Lock()
	local := s.local
	s.mu.Unlock()
	if local != nil {
		all = append(all, local.Close(ctx))
	}
	if s.Hub != nil {
		all = append(all, s.Hub.Close())
	}
	if s.HTTP != nil {
		all = append(all, s.HTTP.Shutdown(ctx))
	}
	// Shutdown owns serving listeners, but New may also be closed before Start.
	for _, listener := range []net.Listener{s.Listener, s.loopbackListener} {
		if listener != nil {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				all = append(all, err)
			}
		}
	}
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		all = append(all, ctx.Err())
	}
	if s.CoreStore != nil {
		all = append(all, s.CoreStore.Close())
	}
	if s.Store != nil {
		all = append(all, s.Store.Close())
	}
	if s.localLock != nil {
		all = append(all, s.localLock.Close())
	}
	if s.lock != nil {
		_ = os.Remove(filepath.Join(s.Config.DataDirectory, "running.json"))
		all = append(all, s.lock.Close())
	}
	return errors.Join(all...)
}
func (s *Server) UseLocal(ctx context.Context) (domain.ComputerStatus, error) {
	status, err := s.useLocal(ctx, true)
	if err == nil {
		s.mu.Lock()
		s.startupError = ""
		s.mu.Unlock()
	}
	return status, err
}

// Re-enrollment is an explicit owner action. Starting the Server may reconnect
// an existing identity, but must never recreate a deleted registration.
func (s *Server) useLocal(ctx context.Context, reenroll bool) (domain.ComputerStatus, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return domain.ComputerStatus{}, domain.Offline("Server is shutting down")
	}
	if s.local != nil {
		id := s.local.Credentials.ComputerID
		var record domain.Computer
		err := s.Store.Get(ctx, "computers", id, &record)
		if err == nil && !record.Revoked {
			s.mu.Unlock()
			return s.waitComputer(ctx, id)
		}
		if !domain.IsMissing(err) || !reenroll {
			s.mu.Unlock()
			if err != nil && !domain.IsMissing(err) {
				return domain.ComputerStatus{}, err
			}
			return domain.ComputerStatus{}, unavailableLocalRegistration(record.Revoked)
		}
		// The registration is gone and the owner explicitly requested a new
		// connection. Keep its data directory while retiring the old boot.
		s.localCancel()
		if err := s.local.Close(ctx); err != nil {
			s.mu.Unlock()
			return domain.ComputerStatus{}, err
		}
		select {
		case <-s.localDone:
		case <-ctx.Done():
			s.mu.Unlock()
			return domain.ComputerStatus{}, ctx.Err()
		}
		if err := s.localLock.Close(); err != nil {
			s.mu.Unlock()
			return domain.ComputerStatus{}, err
		}
		s.local, s.localLock, s.localCancel, s.localDone = nil, nil, nil, nil
	}
	root := filepath.Join(s.Config.ComputerDirectory, "local")
	lock, err := Lock(filepath.Join(root, "daemon.lock"))
	if err != nil {
		s.mu.Unlock()
		return domain.ComputerStatus{}, err
	}
	credentialsPath := filepath.Join(root, "credentials.json")
	var credentials computers.Credentials
	if readErr := ReadJSON(credentialsPath, &credentials); readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		lock.Close()
		s.mu.Unlock()
		return domain.ComputerStatus{}, readErr
	}
	// Local port changes do not create another Computer identity.
	localURL := s.URL
	credentials.ServerURL = localURL
	credentials.ServerPin = s.Hub.ServerPin()
	d, err := computers.NewDaemon(root, credentials)
	if err != nil {
		lock.Close()
		s.mu.Unlock()
		return domain.ComputerStatus{}, err
	}
	d.Hello.Version = s.Config.Version
	if credentials.ComputerID != "" {
		var record domain.Computer
		err = s.Store.Get(ctx, "computers", credentials.ComputerID, &record)
		if domain.IsMissing(err) && reenroll {
			credentials = computers.Credentials{ServerURL: localURL, ServerPin: s.Hub.ServerPin()}
		} else if err != nil || record.Revoked {
			lock.Close()
			s.mu.Unlock()
			if err != nil && !domain.IsMissing(err) {
				return domain.ComputerStatus{}, err
			}
			return domain.ComputerStatus{}, unavailableLocalRegistration(record.Revoked)
		}
	}
	if credentials.ComputerID == "" {
		enrollment, enrollErr := s.Hub.Enroll(ctx, localURL, false, true)
		if enrollErr != nil {
			lock.Close()
			s.mu.Unlock()
			return domain.ComputerStatus{}, enrollErr
		}
		credentials, err = s.Hub.Register(ctx, enrollment.Enrollment.ID, computers.EnrollmentRequest{Token: enrollment.Token, Hello: d.Hello})
		if err != nil {
			lock.Close()
			s.mu.Unlock()
			return domain.ComputerStatus{}, err
		}
		credentials.ServerURL = localURL
		credentials.ServerPin = s.Hub.ServerPin()
	}
	if err = WriteJSON(credentialsPath, credentials); err != nil {
		lock.Close()
		s.mu.Unlock()
		return domain.ComputerStatus{}, err
	}
	d.Credentials = credentials
	s.local, s.localLock = d, lock
	localCtx, stopLocal := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.localCancel, s.localDone = stopLocal, done
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer close(done)
		_ = d.Run(localCtx, nil)
	}()
	s.mu.Unlock()
	return s.waitComputer(ctx, credentials.ComputerID)
}

func unavailableLocalRegistration(revoked bool) error {
	if revoked {
		return domain.Conflict("本机连接已撤销，请先删除电脑，再点击使用本机重新接入。")
	}
	return domain.Conflict("本机连接已删除，点击使用本机重新接入。")
}

func (s *Server) waitComputer(ctx context.Context, id string) (domain.ComputerStatus, error) {
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		var c domain.Computer
		if err := s.Store.Get(ctx, "computers", id, &c); err != nil {
			return domain.ComputerStatus{}, err
		}
		status := s.Hub.Status(c)
		status.TokenID = ""
		if status.Online {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, domain.Offline("Computer registered but has not connected yet")
		case <-timer.C:
		}
	}
}
func (s *Server) web() http.Handler {
	var source fs.FS
	if s.Config.WebDirectory != "" {
		source = os.DirFS(s.Config.WebDirectory)
	} else {
		source, _ = fs.Sub(embeddedWeb, "web")
	}
	files := http.FileServer(http.FS(source))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(source, name); err != nil {
			if strings.Contains(filepath.Base(name), ".") {
				http.NotFound(w, r)
				return
			}
			content, readErr := fs.ReadFile(source, "index.html")
			if readErr != nil {
				http.Error(w, "Web UI is unavailable. Build apps/web and restart with --web-dir apps/web/dist.", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(content)
			return
		}
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		files.ServeHTTP(w, r)
	})
}
func (s *Server) HostInfo(ctx context.Context) domain.HostInfo {
	hostname, _ := os.Hostname()
	info := domain.HostInfo{Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH}
	s.mu.Lock()
	info.LocalError = s.startupError
	s.mu.Unlock()
	// Bootstrap reports optional container availability. Do not give this
	// background hint the longer budget used for an explicit container action.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	probe, err := ProbeDocker(ctx)
	if err != nil {
		info.ContainerError = err.Error()
	} else {
		info.ContainerRuntime = probe.Name
		info.ContainerAvailable = true
	}
	return info
}

package platform

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/oines/astral/internal/domain"
)

type DockerInfo struct {
	Name     string
	Arch     string
	Context  string
	Endpoint string
	Desktop  bool
}

type dockerTargetKey struct{}

func dockerContext(ctx context.Context, info DockerInfo) context.Context {
	return context.WithValue(ctx, dockerTargetKey{}, info)
}
func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	info, fixed := ctx.Value(dockerTargetKey{}).(DockerInfo)
	if fixed {
		args = append([]string{"--host", info.Endpoint}, args...)
	}
	executable, lookupErr := dockerExecutable()
	command := exec.CommandContext(ctx, executable, args...)
	if lookupErr != nil {
		command.Err = lookupErr
	}
	if fixed {
		for _, value := range os.Environ() {
			key, _, _ := strings.Cut(value, "=")
			switch strings.ToUpper(key) {
			case "DOCKER_CONTEXT", "DOCKER_HOST", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "BUILDX_BUILDER", "BUILDKIT_HOST":
				continue
			}
			command.Env = append(command.Env, value)
		}
	}
	return command
}

func dockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	command := dockerCommand(ctx, args...)
	raw, err := command.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(raw))
		if len(text) > 1500 {
			text = text[len(text)-1500:]
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if text == "" {
			return nil, fmt.Errorf("docker %s: %w", args[0], err)
		}
		return nil, fmt.Errorf("docker %s: %w: %s", args[0], err, text)
	}
	return raw, nil
}
func ProbeDocker(ctx context.Context) (DockerInfo, error) {
	if fixed, ok := ctx.Value(dockerTargetKey{}).(DockerInfo); ok {
		if !localDockerEndpoint(fixed.Endpoint) {
			return DockerInfo{}, domain.Invalid("Docker endpoint must be local")
		}
		return fixed, nil
	}
	if _, err := dockerExecutable(); err != nil {
		message := "Install and start Docker Engine before creating a Computer container."
		switch runtime.GOOS {
		case "windows":
			message = "Install and start Docker Desktop in Linux-container mode before creating a Computer container."
		case "darwin":
			message = "Install and start OrbStack or Docker Desktop before creating a Computer container."
		}
		return DockerInfo{}, domain.Offline(message)
	}
	// Context inspection and version both start the Docker CLI. Allow cold starts
	// and a busy local runtime without treating it as an unavailable engine.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var raw []byte
	var err error
	var dockerContext struct {
		Name      string
		Endpoints map[string]struct{ Host string }
	}
	endpoint := os.Getenv("DOCKER_HOST")
	selected := os.Getenv("DOCKER_CONTEXT")
	if selected != "" || endpoint == "" {
		args := []string{"context", "inspect"}
		if selected != "" {
			args = append(args, selected)
		}
		args = append(args, "--format", "{{json .}}")
		raw, err = dockerOutput(ctx, args...)
		if err != nil {
			return DockerInfo{}, domain.Offline("Start the local container runtime. " + err.Error())
		}
		if json.Unmarshal(raw, &dockerContext) != nil {
			return DockerInfo{}, domain.Invalid("Docker returned an invalid context")
		}
		endpoint = dockerContext.Endpoints["docker"].Host
	}
	if !localDockerEndpoint(endpoint) {
		return DockerInfo{}, domain.Invalid("select a local Docker context for this shortcut; remote Docker endpoints are not the Server's host")
	}
	info := DockerInfo{Context: dockerContext.Name, Endpoint: endpoint}
	ctx = dockerContextForProbe(ctx, info)
	// Unlike info, version does not enumerate CLI plugins. A canceled info
	// command can leave a metadata plugin holding this process's cwd on Windows.
	raw, err = dockerOutput(ctx, "version", "--format", "{{json .Server}}")
	if err != nil {
		return DockerInfo{}, domain.Offline("Start the local container runtime. " + err.Error())
	}
	var wire struct {
		OS            string `json:"Os"`
		Arch          string
		KernelVersion string
		Platform      struct{ Name string }
	}
	if json.Unmarshal(raw, &wire) != nil || wire.OS != "linux" {
		return DockerInfo{}, domain.Invalid("a Linux container runtime is required")
	}
	arch := wire.Arch
	switch arch {
	case "aarch64":
		arch = "arm64"
	case "x86_64":
		arch = "amd64"
	}
	if arch != "arm64" && arch != "amd64" {
		return DockerInfo{}, domain.Invalid("container architecture must be arm64 or amd64")
	}
	info.Name, info.Arch = strings.TrimSpace(wire.Platform.Name), arch
	if strings.Contains(strings.ToLower(wire.KernelVersion), "orbstack") {
		info.Name = "OrbStack"
	}
	if info.Name == "" {
		info.Name = "Docker Engine"
	}
	info.Desktop = strings.Contains(strings.ToLower(info.Name), "docker desktop")
	return info, nil
}

// Keep context selection out of subsequent operations: both environment
// overrides and the user's default context may change during a long build.
func dockerContextForProbe(ctx context.Context, info DockerInfo) context.Context {
	return context.WithValue(ctx, dockerTargetKey{}, info)
}
func localDockerEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	if parsed.Scheme == "unix" {
		return parsed.Host == "" && strings.HasPrefix(parsed.Path, "/")
	}
	if parsed.Scheme == "npipe" {
		normal := strings.ReplaceAll(strings.ToLower(endpoint), "\\", "/")
		prefix := "npipe:////./pipe/"
		return strings.HasPrefix(normal, prefix) && len(normal) > len(prefix) && !strings.ContainsAny(normal[len(prefix):], "/\x00\r\n")
	}
	return false
}

type DockerOptions struct {
	Identity       string
	Binary         string
	EnrollmentFile string
	Runtime        *DockerInfo
	// RefreshEnrollment remounts an atomically replaced enrollment file in an
	// existing container. Starting an already running container is a no-op.
	RefreshEnrollment bool
}

// ContainerInstallation records the local provisioning target for explicit
// future updates. It contains no enrollment token or Computer credentials.
type ContainerInstallation struct {
	ID       string `json:"id"`
	Identity string `json:"identity"`
	Endpoint string `json:"endpoint"`
	Context  string `json:"context,omitempty"`
	Name     string `json:"name"`
	Image    string `json:"image"`
}

func dockerBindMount(source, target string) string {
	var buffer strings.Builder
	w := csv.NewWriter(&buffer)
	_ = w.Write([]string{"type=bind", "src=" + source, "dst=" + target, "readonly"})
	w.Flush()
	return strings.TrimSuffix(buffer.String(), "\n")
}

func findExistingContainer(ctx context.Context, identity string) (string, error) {
	name := "astral-" + identity
	raw, err := dockerOutput(ctx, "container", "ls", "-a", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", nil
	}
	label, err := dockerOutput(ctx, "inspect", "--format", `{{index .Config.Labels "astral.operation"}}`, id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(label)) != identity {
		return "", domain.Conflict("the container name belongs to another operation")
	}
	return id, nil
}

// Older local containers have no reported metadata, but their creation records
// retain the exact naming and volume identity. Recover only those known values;
// host/image details must come from the creator, not from the current Server.
func (s *Server) restoreContainerMetadata(ctx context.Context) error {
	records, err := s.Store.List(ctx, "provisioning")
	if err != nil {
		return err
	}
	for _, raw := range records {
		var operation domain.Provisioning
		if err := json.Unmarshal(raw.Data, &operation); err != nil {
			return err
		}
		if operation.Kind != "container" || operation.ComputerID == "" {
			continue
		}
		var computer domain.Computer
		if err := s.Store.Get(ctx, "computers", operation.ComputerID, &computer); err != nil {
			if domain.IsMissing(err) {
				continue
			}
			return err
		}
		if computer.Kind != "container" || computer.Container != nil {
			continue
		}
		name := "astral-" + operation.ID
		computer.Container = &domain.ContainerInfo{Name: name, Volume: name + "-data", MountPath: "/var/lib/astral"}
		if computer.Root == "" {
			computer.Root = "/var/lib/astral"
		}
		if err := s.Store.Save(ctx, "computers", &computer.Meta, &computer); err != nil {
			return err
		}
	}
	return nil
}

type lineOutput struct {
	mu      sync.Mutex
	pending []byte
	emit    func(string)
}

func (w *lineOutput) Write(raw []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, raw...)
	for {
		index := strings.IndexByte(string(w.pending), '\n')
		if index < 0 {
			break
		}
		if w.emit != nil {
			w.emit(strings.TrimSpace(string(w.pending[:index])))
		}
		w.pending = w.pending[index+1:]
	}
	if len(w.pending) > 8192 {
		if w.emit != nil {
			w.emit(string(w.pending[:8192]))
		}
		w.pending = nil
	}
	return len(raw), nil
}
func runDocker(ctx context.Context, emit func(string), args ...string) error {
	command := dockerCommand(ctx, args...)
	output := &lineOutput{emit: emit}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("docker %s failed: %w", args[0], err)
	}
	return nil
}

// LaunchContainer is shared by the local shortcut and the command installer.
// Its stable name and labels make an interrupted create safe to reconcile.
func LaunchContainer(ctx context.Context, options DockerOptions, report func(phase, line string)) (string, error) {
	var probe DockerInfo
	var err error
	if options.Runtime != nil {
		probe = *options.Runtime
		if !localDockerEndpoint(probe.Endpoint) {
			return "", domain.Invalid("Docker endpoint must be local")
		}
	} else {
		probe, err = ProbeDocker(ctx)
		if err != nil {
			return "", err
		}
	}
	ctx = dockerContext(ctx, probe)
	name := "astral-" + options.Identity
	reportLine := func(phase string) func(string) {
		return func(line string) {
			if report != nil && line != "" {
				report(phase, line)
			}
		}
	}
	id, err := findExistingContainer(ctx, options.Identity)
	if err != nil {
		return "", err
	}
	if id != "" {
		action := "start"
		if options.RefreshEnrollment {
			action = "restart"
		}
		if err = runDocker(ctx, reportLine("starting"), action, id); err != nil {
			return id, err
		}
		return id, nil
	}
	binary, err := os.Open(options.Binary)
	if err != nil {
		return "", fmt.Errorf("linux daemon binary is unavailable; build the release distribution: %w", err)
	}
	defer binary.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, binary); err != nil {
		return "", err
	}
	_, _ = hash.Write([]byte(computerDockerfile))
	image := "astral-computer:" + hex.EncodeToString(hash.Sum(nil)[:8])
	contextDir, err := os.MkdirTemp("", "astral-image-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(contextDir)
	if _, err = binary.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	target, err := os.OpenFile(filepath.Join(contextDir, "astral"), os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(target, binary)
	err = errors.Join(err, target.Close())
	if err != nil {
		return "", err
	}
	dockerfile := computerDockerfile
	if err = os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
		return "", err
	}
	if err = runDocker(ctx, reportLine("building_image"), "build", "--progress=plain", "--platform", "linux/"+probe.Arch, "-t", image, contextDir); err != nil {
		return "", err
	}
	volume := name + "-data"
	if err = runDocker(ctx, reportLine("creating_volume"), "volume", "create", "--label", "astral.operation="+options.Identity, volume); err != nil {
		return "", err
	}
	label, err := dockerOutput(ctx, "volume", "inspect", "--format", `{{index .Labels "astral.operation"}}`, volume)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(label)) != options.Identity {
		return "", domain.Conflict("the data volume belongs to another operation")
	}
	// Native Linux and OrbStack retain host networking. Docker Desktop uses its
	// ordinary bridge and the authenticated tunnel on the main Server listener.
	host, err := os.Hostname()
	if err != nil {
		return "", err
	}
	metadata, err := json.Marshal(domain.ContainerInfo{Name: name, Host: host, Image: image, Volume: volume, MountPath: "/var/lib/astral"})
	if err != nil {
		return "", err
	}
	network := "host"
	if probe.Desktop && (runtime.GOOS == "windows" || runtime.GOOS == "darwin") {
		network = "bridge"
	}
	args := []string{"create", "--name", name, "--init", "--restart", "unless-stopped", "--network", network,
		"--label", "astral.operation=" + options.Identity,
		"--env", "ASTRAL_CONTAINER_INFO=" + string(metadata),
		"--mount", "type=volume,src=" + volume + ",dst=/var/lib/astral",
		"--mount", dockerBindMount(options.EnrollmentFile, "/run/astral-enrollment.json"),
		image, "computer", "supervise", "--enrollment-file", "/run/astral-enrollment.json", "--root", "/var/lib/astral"}
	raw, err := dockerOutput(ctx, args...)
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(string(raw))
	if err = WriteJSON(filepath.Join(filepath.Dir(options.EnrollmentFile), "container.json"), ContainerInstallation{ID: id, Identity: options.Identity, Endpoint: probe.Endpoint, Context: probe.Context, Name: name, Image: image}); err != nil {
		return id, err
	}
	if report != nil {
		report("starting", "Container created: "+id)
	}
	if err = runDocker(ctx, reportLine("starting"), "start", id); err != nil {
		return id, err
	}
	return id, nil
}
func (s *Server) CreateContainer(ctx context.Context, intent string) (domain.Provisioning, error) {
	if len(intent) > 200 {
		return domain.Provisioning{}, domain.Invalid("provisioning request ID is too long")
	}
	sum := sha256.Sum256([]byte(s.Config.DataDirectory + "\x00" + intent))
	id := "container_" + hex.EncodeToString(sum[:12])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return domain.Provisioning{}, domain.Offline("Server is shutting down")
	}
	var operation domain.Provisioning
	err := s.Store.Get(ctx, "provisioning", id, &operation)
	if err != nil && !domain.IsMissing(err) {
		return operation, err
	}
	if err == nil && (operation.State == "connected" || s.provisioning[id]) {
		return operation, nil
	}
	if domain.IsMissing(err) {
		operation = domain.Provisioning{Meta: domain.Meta{ID: id}, IntentID: intent, Kind: "container", State: "preparing", Phase: "checking_runtime", Log: []string{}}
	}
	operation.State, operation.Error = "preparing", ""
	if err = s.Store.Save(ctx, "provisioning", &operation.Meta, &operation); err != nil {
		return operation, err
	}
	s.provisioning[id] = true
	s.workers.Add(1)
	go func(value domain.Provisioning) {
		defer s.workers.Done()
		defer func() { s.mu.Lock(); delete(s.provisioning, id); s.mu.Unlock() }()
		s.provision(value)
	}(operation)
	return operation, nil
}
func (s *Server) provision(operation domain.Provisioning) {
	// The complete workspace image can take time on a first install or a slow
	// network. Leave ten minutes inside the fixed 90-minute enrollment window
	// for the actual Daemon registration and online confirmation.
	ctx, cancel := context.WithTimeout(s.ctx, 80*time.Minute)
	defer cancel()
	save := func() {
		persist, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		_ = s.Store.Save(persist, "provisioning", &operation.Meta, &operation)
	}
	fail := func(err error) {
		operation.State, operation.Phase, operation.Error = "failed", "failed", err.Error()
		save()
	}
	report := func(phase, line string) {
		operation.Phase = phase
		operation.State = "creating"
		operation.Log = append(operation.Log, line)
		if len(operation.Log) > 80 {
			operation.Log = operation.Log[len(operation.Log)-80:]
		}
		save()
	}
	probe, err := ProbeDocker(ctx)
	if err != nil {
		fail(err)
		return
	}
	ctx = dockerContext(ctx, probe)
	binary, err := s.binary("linux", probe.Arch)
	if err != nil {
		fail(err)
		return
	}
	root := filepath.Join(s.Config.ComputerDirectory, "provisioning", operation.ID)
	enrollmentPath := filepath.Join(root, "enrollment.json")
	var enrollment ConnectOptions
	alreadyRegistered := false
	refreshEnrollment := false
	if operation.EnrollmentID != "" {
		if err = ReadJSON(enrollmentPath, &enrollment); err != nil {
			fail(err)
			return
		}
		// Registered identities remain in their data volume. An expired unused
		// pairing is replaced; retry must never resurrect a previously leaked token.
		var record domain.Enrollment
		if err = s.Store.Get(ctx, "enrollments", operation.EnrollmentID, &record); err != nil {
			fail(err)
			return
		}
		alreadyRegistered = record.State == "registered"
		if !alreadyRegistered && time.Now().After(record.ExpiresAt) {
			operation.EnrollmentID = ""
			refreshEnrollment = true
		}
	}
	if operation.EnrollmentID == "" {
		localURL := s.URL
		if probe.Desktop && (runtime.GOOS == "windows" || runtime.GOOS == "darwin") {
			localURL = s.ContainerURL()
			if localURL == "" {
				fail(domain.Invalid("Docker Desktop requires the Astral Server main listener to be reachable from the container; use the default wildcard listen address"))
				return
			}
		}
		result, enrollErr := s.Hub.Enroll(ctx, localURL, true, false)
		if enrollErr != nil {
			fail(enrollErr)
			return
		}
		operation.EnrollmentID = result.Enrollment.ID
		enrollment = ConnectOptions{ServerURL: localURL, ServerPin: result.ServerPin, EnrollmentID: result.Enrollment.ID, Token: result.Token}
		if err = WriteJSON(enrollmentPath, enrollment); err != nil {
			fail(err)
			return
		}
		save()
	}
	if !alreadyRegistered {
		if err = s.Hub.PrepareContainer(ctx, enrollment.EnrollmentID, enrollment.Token); err != nil {
			fail(err)
			return
		}
	}
	id, err := LaunchContainer(ctx, DockerOptions{Identity: operation.ID, Binary: binary, EnrollmentFile: enrollmentPath, Runtime: &probe, RefreshEnrollment: refreshEnrollment}, report)
	operation.ContainerID = id
	if err != nil {
		fail(err)
		return
	}
	operation.State, operation.Phase = "connecting", "waiting_for_daemon"
	save()
	connectCtx, done := context.WithTimeout(ctx, 45*time.Second)
	defer done()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		var record domain.Enrollment
		if err = s.Store.Get(connectCtx, "enrollments", operation.EnrollmentID, &record); err == nil && record.ComputerID != "" {
			operation.ComputerID = record.ComputerID
			status, waitErr := s.waitComputer(connectCtx, record.ComputerID)
			if waitErr == nil && status.Online {
				operation.State, operation.Phase = "connected", "connected"
				save()
				return
			}
		}
		select {
		case <-connectCtx.Done():
			logs, _ := dockerOutput(ctx, "logs", "--tail", "20", id)
			scanner := bufio.NewScanner(strings.NewReader(string(logs)))
			for scanner.Scan() {
				report("waiting_for_daemon", scanner.Text())
			}
			fail(domain.Offline("Container started but its Daemon did not connect. Check its log, Server URL, and local container networking or firewall."))
			return
		case <-tick.C:
		}
	}
}

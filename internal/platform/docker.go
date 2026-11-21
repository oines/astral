package platform

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
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
	Name          string
	Arch          string
	Context       string
	Endpoint      string
	Platform      string
	KernelVersion string
	Desktop       bool
	execution     *dockerExecution
}

type dockerTargetKey struct{}

func dockerContext(ctx context.Context, info DockerInfo) context.Context {
	info.execution = dockerExecutionFor(ctx, info)
	return context.WithValue(ctx, dockerTargetKey{}, info)
}
func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	info, fixed := ctx.Value(dockerTargetKey{}).(DockerInfo)
	execution := dockerExecutionFor(ctx, info)
	if fixed {
		args = append([]string{"--host", info.Endpoint}, args...)
	}
	command := exec.CommandContext(ctx, execution.executable, args...)
	if execution.err != nil {
		command.Err = execution.err
	}
	command.Env = append([]string{}, execution.env...)
	if fixed {
		command.Env = fixedDockerEnvironment(command.Env)
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
	execution := currentDockerExecution()
	if execution.err != nil {
		return DockerInfo{}, dockerProbeFailure(execution.err)
	}
	// Context inspection and version both start the Docker CLI. Allow cold starts
	// and a busy local runtime without treating it as an unavailable engine.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, dockerExecutionKey{}, execution)
	var raw []byte
	var err error
	var dockerContext struct {
		Name      string
		Endpoints map[string]struct{ Host string }
	}
	endpoint := dockerEnvironmentValue(runtime.GOOS, execution.env, "DOCKER_HOST")
	selected := dockerEnvironmentValue(runtime.GOOS, execution.env, "DOCKER_CONTEXT")
	if selected != "" || endpoint == "" {
		args := []string{"context", "inspect"}
		if selected != "" {
			args = append(args, selected)
		}
		args = append(args, "--format", "{{json .}}")
		raw, err = dockerOutput(ctx, args...)
		if err != nil {
			return DockerInfo{}, dockerProbeFailure(err)
		}
		if err = json.Unmarshal(raw, &dockerContext); err != nil {
			return DockerInfo{}, domain.Invalid("Docker returned an invalid context: " + err.Error())
		}
		endpoint = dockerContext.Endpoints["docker"].Host
	}
	if !localDockerEndpoint(endpoint) {
		return DockerInfo{}, domain.Invalid("select a local Docker context for this shortcut; remote Docker endpoints are not the Server's host")
	}
	info := DockerInfo{Context: dockerContext.Name, Endpoint: endpoint, execution: execution}
	ctx = dockerContextForProbe(ctx, info)
	// Unlike info, version does not enumerate CLI plugins. A canceled info
	// command can leave a metadata plugin holding this process's cwd on Windows.
	raw, err = dockerOutput(ctx, "version", "--format", "{{json .Server}}")
	if err != nil {
		return DockerInfo{}, dockerProbeFailure(err)
	}
	var wire struct {
		OS            string `json:"Os"`
		Arch          string
		KernelVersion string
		Platform      struct{ Name string }
	}
	if err = json.Unmarshal(raw, &wire); err != nil {
		return DockerInfo{}, domain.Invalid("Docker returned an invalid Server version: " + err.Error())
	}
	if wire.OS != "linux" {
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
	info.Platform = strings.TrimSpace(wire.Platform.Name)
	info.KernelVersion = wire.KernelVersion
	info.Name, info.Arch = info.Platform, arch
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
	return dockerContext(ctx, info)
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
	ServerURLs     []string
	// LocalServerURL is set only by the Server's local-container shortcut. It
	// excludes public/LAN enrollment routes without changing remote CLI setup.
	LocalServerURL string
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
		if err = injectCreatedContainer(ctx, id, options.Binary, reportLine("injecting_daemon")); err != nil {
			return id, err
		}
		action := "start"
		if options.RefreshEnrollment {
			action = "restart"
		}
		if err = runDocker(ctx, reportLine("starting"), action, id); err != nil {
			return id, err
		}
		return id, nil
	}
	if info, statErr := os.Stat(options.Binary); statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("the Linux Daemon executable is unavailable: %s", options.Binary)
	}
	image, err := computerImageReference()
	if err != nil {
		return "", err
	}
	if err = ensureComputerImage(ctx, image, probe.Arch, reportLine("pulling_image")); err != nil {
		return "", err
	}
	var enrollment ConnectOptions
	if err = ReadJSON(options.EnrollmentFile, &enrollment); err != nil {
		return "", err
	}
	if report != nil {
		report("checking_network", "Checking container access to the Server.")
	}
	var network, serverURL string
	addHostGateway := false
	if options.LocalServerURL != "" {
		route, routeErr := localContainerRoute(ctx, image, probe.Arch, options.Binary, options.LocalServerURL, enrollment.ServerPin, reportLine("checking_network"))
		if routeErr != nil {
			return "", routeErr
		}
		network, serverURL, addHostGateway = route.network, route.serverURL, route.addHostGateway
	} else {
		network, serverURL, err = containerRoute(ctx, image, probe.Arch, options.Binary, enrollment, options.ServerURLs, reportLine("checking_network"))
		if err != nil {
			return "", err
		}
		addHostGateway = network == "bridge"
	}
	if serverURL != enrollment.ServerURL {
		enrollment.ServerURL = serverURL
		if err = WriteJSON(options.EnrollmentFile, enrollment); err != nil {
			return "", err
		}
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
	host, err := os.Hostname()
	if err != nil {
		return "", err
	}
	metadata, err := json.Marshal(domain.ContainerInfo{Name: name, Host: host, Image: image, Volume: volume, MountPath: "/var/lib/astral"})
	if err != nil {
		return "", err
	}
	args := []string{"create", "--pull", "never", "--platform", "linux/" + probe.Arch, "--name", name, "--init", "--restart", "unless-stopped", "--network", network,
		"--entrypoint", "/usr/local/bin/astral",
		"--label", "astral.operation=" + options.Identity,
		"--env", "ASTRAL_CONTAINER_INFO=" + string(metadata),
		"--mount", "type=volume,src=" + volume + ",dst=/var/lib/astral",
		"--mount", dockerBindMount(options.EnrollmentFile, "/run/astral-enrollment.json"),
		image, "computer", "supervise", "--enrollment-file", "/run/astral-enrollment.json", "--root", "/var/lib/astral"}
	if addHostGateway {
		args = append([]string{"create", "--add-host", "astral.host.internal:host-gateway"}, args[1:]...)
	}
	if report != nil {
		report("creating_container", "Creating the Computer container.")
	}
	raw, err := dockerOutput(ctx, args...)
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(string(raw))
	if err = WriteJSON(filepath.Join(filepath.Dir(options.EnrollmentFile), "container.json"), ContainerInstallation{ID: id, Identity: options.Identity, Endpoint: probe.Endpoint, Context: probe.Context, Name: name, Image: image}); err != nil {
		return id, err
	}
	if report != nil {
		report("injecting_daemon", "Installing the Daemon into the container.")
	}
	if err = injectContainerDaemon(ctx, id, options.Binary); err != nil {
		return id, err
	}
	if err = runDocker(ctx, reportLine("starting"), "start", id); err != nil {
		return id, err
	}
	return id, nil
}

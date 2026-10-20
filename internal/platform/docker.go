package platform

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oines/astral/internal/domain"
)

type DockerInfo struct {
	Name    string
	Arch    string
	Context string
}

func dockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	raw, err := command.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(raw))
		if len(text) > 1500 {
			text = text[len(text)-1500:]
		}
		return nil, fmt.Errorf("Docker: %s", text)
	}
	return raw, nil
}
func ProbeDocker(ctx context.Context) (DockerInfo, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return DockerInfo{}, domain.Offline("Install and start OrbStack or Docker before creating a Computer container.")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := dockerOutput(ctx, "context", "inspect", "--format", "{{json .}}")
	if err != nil {
		return DockerInfo{}, domain.Offline("Start the local container runtime. " + err.Error())
	}
	var dockerContext struct {
		Name      string
		Endpoints map[string]struct{ Host string }
	}
	if json.Unmarshal(raw, &dockerContext) != nil {
		return DockerInfo{}, domain.Invalid("Docker returned an invalid context")
	}
	if !strings.HasPrefix(dockerContext.Endpoints["docker"].Host, "unix://") {
		return DockerInfo{}, domain.Invalid("select a local Docker context for this shortcut; remote Docker endpoints are not the Server's host")
	}
	raw, err = dockerOutput(ctx, "info", "--format", "{{json .}}")
	if err != nil {
		return DockerInfo{}, domain.Offline("Start the local container runtime. " + err.Error())
	}
	var wire struct {
		Name            string
		OSType          string
		Architecture    string
		OperatingSystem string
	}
	if json.Unmarshal(raw, &wire) != nil || wire.OSType != "linux" {
		return DockerInfo{}, domain.Invalid("a Linux container runtime is required")
	}
	arch := wire.Architecture
	switch arch {
	case "aarch64":
		arch = "arm64"
	case "x86_64":
		arch = "amd64"
	}
	if arch != "arm64" && arch != "amd64" {
		return DockerInfo{}, domain.Invalid("container architecture must be arm64 or amd64")
	}
	return DockerInfo{Name: wire.OperatingSystem, Context: dockerContext.Name, Arch: arch}, nil
}

type DockerOptions struct {
	Identity       string
	Binary         string
	EnrollmentFile string
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
	command := exec.CommandContext(ctx, "docker", args...)
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
	probe, err := ProbeDocker(ctx)
	if err != nil {
		return "", err
	}
	name := "astral-" + options.Identity
	reportLine := func(phase string) func(string) {
		return func(line string) {
			if report != nil && line != "" {
				report(phase, line)
			}
		}
	}
	raw, err := dockerOutput(ctx, "container", "ls", "-a", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if id != "" {
		label, inspectErr := dockerOutput(ctx, "inspect", "--format", `{{index .Config.Labels "astral.operation"}}`, id)
		if inspectErr != nil {
			return "", inspectErr
		}
		if strings.TrimSpace(string(label)) != options.Identity {
			return "", domain.Conflict("the container name belongs to another operation")
		}
		if err = runDocker(ctx, reportLine("starting"), "start", id); err != nil {
			return id, err
		}
		return id, nil
	}
	binary, err := os.Open(options.Binary)
	if err != nil {
		return "", fmt.Errorf("Linux Daemon binary is unavailable; build the release distribution: %w", err)
	}
	defer binary.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, binary); err != nil {
		return "", err
	}
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
	dockerfile := `FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends bash ca-certificates curl git python3 ripgrep procps && rm -rf /var/lib/apt/lists/*
COPY --chmod=755 astral /usr/local/bin/astral
ENV HOME=/root SHELL=/bin/bash
WORKDIR /var/lib/astral
ENTRYPOINT ["/usr/local/bin/astral"]
`
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
	// Host networking supports a loopback Server on Linux and OrbStack. Docker
	// Desktop must have its documented host-networking option enabled.
	raw, err = dockerOutput(ctx, "create", "--name", name, "--init", "--restart", "unless-stopped", "--network", "host",
		"--label", "astral.operation="+options.Identity,
		"--mount", "type=volume,src="+volume+",dst=/var/lib/astral",
		"--mount", "type=bind,src="+options.EnrollmentFile+",dst=/run/astral-enrollment.json,readonly",
		image, "computer", "connect", "--enrollment-file", "/run/astral-enrollment.json", "--root", "/var/lib/astral", "--foreground")
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(string(raw))
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
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Minute)
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
	binary, err := s.binary("linux", probe.Arch)
	if err != nil {
		fail(err)
		return
	}
	root := filepath.Join(s.Config.ComputerDirectory, "provisioning", operation.ID)
	enrollmentPath := filepath.Join(root, "enrollment.json")
	var enrollment ConnectOptions
	if operation.EnrollmentID != "" {
		if err = ReadJSON(enrollmentPath, &enrollment); err != nil {
			fail(err)
			return
		}
		// Explicit retry renews the same enrollment and container, not a new
		// Computer. Already registered credentials remain in its data volume.
		var record domain.Enrollment
		if err = s.Store.Get(ctx, "enrollments", operation.EnrollmentID, &record); err != nil {
			fail(err)
			return
		}
		record.ExpiresAt = time.Now().UTC().Add(15 * time.Minute)
		if err = s.Store.Save(ctx, "enrollments", &record.Meta, &record); err != nil {
			fail(err)
			return
		}
	} else {
		result, enrollErr := s.Hub.Enroll(ctx, s.URL, true, false)
		if enrollErr != nil {
			fail(enrollErr)
			return
		}
		operation.EnrollmentID = result.Enrollment.ID
		enrollment = ConnectOptions{ServerURL: s.URL, EnrollmentID: result.Enrollment.ID, Token: result.Token}
		if err = WriteJSON(enrollmentPath, enrollment); err != nil {
			fail(err)
			return
		}
		save()
	}
	id, err := LaunchContainer(ctx, DockerOptions{Identity: operation.ID, Binary: binary, EnrollmentFile: enrollmentPath}, report)
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
			fail(domain.Offline("Container started but its Daemon did not connect. Check its log and Server URL. For Docker Desktop, enable host networking."))
			return
		case <-tick.C:
		}
	}
}

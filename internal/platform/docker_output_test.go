package platform

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDockerOutputPreservesProcessFailureAndDiagnostic(t *testing.T) {
	ctx, _, _, _ := containerUpdateFixture(t, "")
	_, err := dockerOutput(ctx, "unexpected-test-command")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(err.Error(), "unexpected container mutation") {
		t.Fatalf("Docker failure lost its exit status or diagnostic: %v", err)
	}
}

func TestDockerOutputPreservesCancellationWithoutOutput(t *testing.T) {
	ctx, _, _, _ := containerUpdateFixture(t, "")
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err := dockerOutput(ctx, "version")
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "docker version") {
		t.Fatalf("Docker cancellation lost its cause or command: %v", err)
	}
}

func TestDockerOutputPreservesMissingExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := dockerOutput(context.Background(), "version")
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing Docker executable lost its cause: %v", err)
	}
}

func TestProbeDockerUsesServerVersionWithoutPluginEnumeration(t *testing.T) {
	for _, test := range []struct {
		name, version, runtimeName, arch, failure string
		desktop                                   bool
	}{
		{name: "Docker Desktop", version: `{"Platform":{"Name":"Docker Desktop 4.19.0 (12345)"},"Os":"linux","Arch":"amd64"}`, runtimeName: "Docker Desktop 4.19.0 (12345)", arch: "amd64", desktop: true},
		{name: "OrbStack", version: `{"Platform":{"Name":"Docker Engine - Community"},"Os":"linux","Arch":"arm64","KernelVersion":"7.2.9-orbstack"}`, runtimeName: "OrbStack", arch: "arm64"},
		{name: "legacy platform", version: `{"Os":"linux","Arch":"x86_64"}`, runtimeName: "Docker Engine", arch: "amd64"},
		{name: "architecture alias", version: `{"Os":"linux","Arch":"aarch64"}`, runtimeName: "Docker Engine", arch: "arm64"},
		{name: "Windows containers", version: `{"Platform":{"Name":"Docker Desktop"},"Os":"windows","Arch":"amd64"}`, failure: "Linux container runtime"},
		{name: "unsupported architecture", version: `{"Os":"linux","Arch":"arm"}`, failure: "architecture must be arm64 or amd64"},
		{name: "missing server", version: `null`, failure: "Linux container runtime"},
		{name: "invalid response", version: `{`, failure: "Linux container runtime"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, statePath, read := containerUpdateFixture(t, "")
			state := read()
			state.ServerVersion = test.version
			if err := WriteJSON(statePath, state); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DOCKER_HOST", "unix:///astral-test.sock")
			t.Setenv("DOCKER_CONTEXT", "")
			info, err := ProbeDocker(context.Background())
			if test.failure != "" {
				if err == nil || !strings.Contains(err.Error(), test.failure) {
					t.Fatalf("expected %q, got %+v, %v", test.failure, info, err)
				}
			} else if err != nil || info.Name != test.runtimeName || info.Arch != test.arch || info.Desktop != test.desktop || info.Endpoint != "unix:///astral-test.sock" {
				t.Fatalf("unexpected Docker runtime: %+v, %v", info, err)
			}
			commands := read().Commands
			if len(commands) != 1 || strings.Join(commands[0], " ") != "--host unix:///astral-test.sock version --format {{json .Server}}" {
				t.Fatalf("probe did not use only the pinned Server version query: %v", commands)
			}
		})
	}
}

func TestHostInfoBoundsSlowDockerWithoutExhaustingBootstrap(t *testing.T) {
	_, _, _, _ = containerUpdateFixture(t, "slow-probe")
	t.Setenv("DOCKER_HOST", "unix:///astral-test.sock")
	t.Setenv("DOCKER_CONTEXT", "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	server := &Server{}
	info := server.HostInfo(ctx)
	if info.ContainerAvailable || !strings.Contains(info.ContainerError, context.DeadlineExceeded.Error()) {
		t.Fatalf("slow Docker did not produce an unavailable hint: %+v", info)
	}
	if ctx.Err() != nil || time.Since(started) > 8*time.Second {
		t.Fatalf("optional Docker availability exhausted the bootstrap request: %v", ctx.Err())
	}
}

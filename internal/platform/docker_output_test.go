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
	_, err := dockerOutput(ctx, "info")
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "docker info") {
		t.Fatalf("Docker cancellation lost its cause or command: %v", err)
	}
}

func TestDockerOutputPreservesMissingExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := dockerOutput(context.Background(), "info")
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing Docker executable lost its cause: %v", err)
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

package platform

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
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

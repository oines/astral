//go:build integration

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerUpdateKeepsContainerAndInstalledEnvironment(t *testing.T) {
	s, ctx := distributionServer(t)
	operation, err := s.CreateContainer(ctx, "integration-inplace-update")
	if err != nil {
		t.Fatal(err)
	}
	removeTestContainer(t, operation.ID)
	await(t, ctx, func() bool {
		return s.Store.Get(ctx, "provisioning", operation.ID, &operation) == nil && (operation.State == "connected" || operation.State == "failed")
	})
	if operation.State != "connected" {
		t.Fatalf("create: %s\n%s", operation.Error, strings.Join(operation.Log, "\n"))
	}
	probe, err := ProbeDocker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ctx = dockerContext(ctx, probe)
	before, err := dockerOutput(ctx, "inspect", "--format", "{{.Id}} {{.Image}} {{json .Mounts}}", operation.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dockerOutput(ctx, "exec", operation.ContainerID, "/bin/sh", "-c", `set -eu; printf '#!/bin/sh\necho agent-installed\n' > /usr/local/bin/agent-custom-tool; chmod 755 /usr/local/bin/agent-custom-tool; printf 'persistent data' > /var/lib/astral/update-marker`); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "astral")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-X github.com/oines/astral/internal/cli.Version=integration-updated", "-o", binary, "./cmd/astral")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+probe.Arch, "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build new release: %v\n%s", err, output)
	}
	if err = UpdateContainerDaemon(ctx, operation.ID, binary, "integration-updated", func(message string) { t.Log(message) }); err != nil {
		t.Fatal(err)
	}
	after, err := dockerOutput(ctx, "inspect", "--format", "{{.Id}} {{.Image}} {{json .Mounts}}", operation.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("update replaced container/image/mounts:\nbefore %s\nafter %s", before, after)
	}
	output, err := dockerOutput(ctx, "exec", operation.ContainerID, "/bin/sh", "-c", `agent-custom-tool; cat /var/lib/astral/update-marker`)
	if err != nil || string(output) != "agent-installed\npersistent data" {
		t.Fatalf("lost installed environment/data: %s %v", output, err)
	}
	// A corrupt or wrong-release executable must fail before replacing the
	// running program, leaving the upgraded Computer connected and usable.
	if err = UpdateContainerDaemon(ctx, operation.ID, binary, "wrong-version", nil); err == nil {
		t.Fatal("accepted the wrong release")
	}
	version, err := dockerOutput(ctx, "exec", operation.ContainerID, "/usr/local/bin/astral", "version")
	if err != nil || strings.TrimSpace(string(version)) != "integration-updated" {
		t.Fatal(fmt.Sprintf("working release changed: %s %v", version, err))
	}
	// This release passes version preflight but cannot start its Daemon. Exercise
	// restoration after replacement/restart, not just preflight rejection.
	broken := filepath.Join(t.TempDir(), "broken-daemon")
	if err = os.WriteFile(broken, []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo broken-update; exit 0; fi\nexit 42\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = UpdateContainerDaemon(ctx, operation.ID, broken, "broken-update", func(message string) { t.Log(message) }); err == nil {
		t.Fatal("accepted a release whose Daemon cannot start")
	}
	version, err = dockerOutput(ctx, "exec", operation.ContainerID, "/usr/local/bin/astral", "version")
	if err != nil || strings.TrimSpace(string(version)) != "integration-updated" {
		t.Fatalf("failed to restore working release: %s %v", version, err)
	}
	after, err = dockerOutput(ctx, "inspect", "--format", "{{.Id}} {{.Image}} {{json .Mounts}}", operation.ContainerID)
	if err != nil || string(before) != string(after) {
		t.Fatalf("rollback replaced original container or mounts: %s %v", after, err)
	}
	output, err = dockerOutput(ctx, "exec", operation.ContainerID, "/bin/sh", "-c", `agent-custom-tool; cat /var/lib/astral/update-marker`)
	if err != nil || string(output) != "agent-installed\npersistent data" {
		t.Fatalf("rollback lost environment: %s %v", output, err)
	}
}

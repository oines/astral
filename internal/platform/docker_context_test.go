package platform

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"
)

func TestDockerEndpointsAndPinnedOperations(t *testing.T) {
	for _, endpoint := range []string{"unix:///var/run/docker.sock", "unix:///Users/user/.docker/run/docker.sock", "npipe:////./pipe/docker_engine", "npipe:////./pipe/dockerDesktopLinuxEngine"} {
		if !localDockerEndpoint(endpoint) {
			t.Errorf("rejected local endpoint %s", endpoint)
		}
	}
	for _, endpoint := range []string{"tcp://127.0.0.1:2375", "ssh://server", "npipe:////remote/pipe/docker_engine", "unix://remote/var/run/docker.sock", "npipe:////./pipe/../other", ""} {
		if localDockerEndpoint(endpoint) {
			t.Errorf("accepted nonlocal/invalid endpoint %s", endpoint)
		}
	}
	t.Setenv("DOCKER_HOST", "tcp://remote:2375")
	t.Setenv("DOCKER_CONTEXT", "other")
	t.Setenv("BUILDX_BUILDER", "remote-builder")
	fixed := DockerInfo{Endpoint: "npipe:////./pipe/docker_engine", Arch: "amd64", Context: "desktop-linux", Desktop: true}
	ctx := dockerContext(context.Background(), fixed)
	probe, err := ProbeDocker(ctx)
	if err != nil || probe.Endpoint != fixed.Endpoint {
		t.Fatalf("pinned probe re-read changing environment: %+v %v", probe, err)
	}
	command := dockerCommand(ctx, "create", "image")
	if len(command.Args) < 4 || command.Args[1] != "--host" || command.Args[2] != fixed.Endpoint {
		t.Fatalf("operation did not bind verified endpoint: %v", command.Args)
	}
	for _, env := range command.Env {
		if strings.HasPrefix(env, "DOCKER_CONTEXT=") || strings.HasPrefix(env, "DOCKER_HOST=") || strings.HasPrefix(env, "BUILDX_BUILDER=") {
			t.Fatalf("conflicting Docker target inherited: %s", env)
		}
	}
}

func TestDockerMountEscapesCSVWithoutReinterpretingWindowsPath(t *testing.T) {
	path := `C:\Users\中文 user,team\Astral\enrollment.json`
	mount := dockerBindMount(path, "/run/astral-enrollment.json")
	reader := csv.NewReader(strings.NewReader(mount))
	fields, err := reader.Read()
	if err != nil || len(fields) != 4 || fields[1] != "src="+path || fields[2] != "dst=/run/astral-enrollment.json" || fields[3] != "readonly" {
		t.Fatalf("mount path was split or changed: %q %v", mount, err)
	}
}

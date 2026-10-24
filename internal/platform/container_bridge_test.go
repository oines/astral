package platform

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/computers"
)

func TestContainerBridgePinsTLSIsolatesOwnerAndSurvivesRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root := t.TempDir()
	config := Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers"), NoLocal: true}
	s, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	address, ca, err := s.ensureContainerBridge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(address)
	parsed.Host = strings.Replace(parsed.Host, "host.docker.internal", "127.0.0.1", 1)
	local := parsed.String()
	client, err := computers.NewHTTPClient(ca)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/api/v1/bootstrap", "/auth/session", "/install.sh", "/downloads/checksums.txt", "/health"} {
		request, _ := http.NewRequestWithContext(ctx, "GET", local+path, nil)
		request.Header.Set("Authorization", "Bearer "+s.token)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 404 {
			t.Fatalf("container listener exposed %s: %d", path, response.StatusCode)
		}
	}
	unenrolled, err := s.Hub.Enroll(ctx, s.URL, true, false)
	if err != nil {
		t.Fatal(err)
	}
	hostname, _ := os.Hostname()
	var access containerAccessResponse
	if err = HTTPJSON(ctx, "POST", s.URL+"/internal/enrollments/"+unenrolled.Enrollment.ID+"/container-access", "wrong", map[string]string{"hostname": hostname}, &access); err == nil {
		t.Fatal("bridge access accepted an invalid enrollment token")
	}
	if err = HTTPJSON(ctx, "POST", s.URL+"/internal/enrollments/"+unenrolled.Enrollment.ID+"/container-access", unenrolled.Token, map[string]string{"hostname": "another-computer"}, &access); err == nil {
		t.Fatal("bridge access accepted a different host")
	}
	if err = HTTPJSON(ctx, "POST", s.URL+"/internal/enrollments/"+unenrolled.Enrollment.ID+"/container-access", unenrolled.Token, map[string]string{"hostname": hostname}, &access); err != nil || access.ServerCA != ca || access.ServerURL != address {
		t.Fatalf("local token exchange failed: %+v %v", access, err)
	}
	initial, err := computers.NewDaemon(filepath.Join(root, "daemon"), computers.Credentials{ServerURL: local, ServerCA: ca})
	if err != nil {
		t.Fatal(err)
	}
	var credentials computers.Credentials
	if err = httpJSONWithCA(ctx, "POST", local+"/internal/enrollments/"+unenrolled.Enrollment.ID, "", computers.EnrollmentRequest{Token: unenrolled.Token, Hello: initial.Hello}, &credentials, ca); err != nil {
		t.Fatal(err)
	}
	credentials.ServerURL, credentials.ServerCA = local, ca
	d, err := computers.NewDaemon(filepath.Join(root, "daemon"), credentials)
	if err != nil {
		t.Fatal(err)
	}
	daemonCtx, stopDaemon := context.WithCancel(ctx)
	defer stopDaemon()
	done := make(chan error, 1)
	go func() { done <- d.Run(daemonCtx, nil) }()
	await(t, ctx, func() bool { status, err := computerStatus(ctx, credentials); return err == nil && status.Online })
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	address2, ca2, err := s.ensureContainerBridge(ctx)
	if err != nil || address2 != address || ca2 != ca {
		t.Fatalf("bridge identity changed after restart: %s %v", address2, err)
	}
	await(t, ctx, func() bool {
		status, err := computerStatus(ctx, credentials)
		return err == nil && status.Online && status.ID == credentials.ComputerID
	})
	stopDaemon()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_ = d.Close(context.Background())
}

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

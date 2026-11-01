package platform

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestDockerProbeFailureClassifiesCauseWithoutLosingIt(t *testing.T) {
	for _, test := range []struct {
		name, status string
		cause        error
	}{
		{"missing CLI", "missing_cli", &exec.Error{Name: "docker", Err: exec.ErrNotFound}},
		{"CLI permission", "permission_denied", fs.ErrPermission},
		{"socket permission", "permission_denied", errors.New("permission denied while trying to connect to the Docker daemon socket")},
		{"Windows permission", "permission_denied", errors.New("open //./pipe/docker_engine: Access is denied.")},
		{"engine stopped", "not_running", errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?")},
		{"missing rootless socket", "not_running", errors.New("dial unix /run/user/1000/docker.sock: connect: no such file or directory")},
		{"missing Windows pipe", "not_running", errors.New("open //./pipe/docker_engine: The system cannot find the file specified.")},
		{"missing context", "error", errors.New("context work: context not found")},
		{"slow engine", "error", context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := dockerProbeFailure(test.cause)
			var failure *dockerProbeError
			var problem *domain.Error
			if !errors.As(err, &failure) || failure.status != test.status || !errors.Is(err, test.cause) {
				t.Fatalf("lost classified Docker cause: %#v", err)
			}
			if !errors.As(err, &problem) || problem.Status != 503 || !strings.Contains(err.Error(), test.cause.Error()) {
				t.Fatalf("lost HTTP status or Docker diagnostic: %v", err)
			}
			if test.status == "permission_denied" && !strings.Contains(err.Error(), failure.user) {
				t.Fatalf("permission error omitted the process account: %v", err)
			}
		})
	}
}

func TestDockerProcessUserDoesNotTrustShellAccountVariables(t *testing.T) {
	t.Setenv("USER", "a-different-login-account")
	t.Setenv("USERNAME", "a-different-login-account")
	actual := dockerProcessUser()
	if actual == "" || actual == "a-different-login-account" {
		t.Fatalf("Docker check used a shell account instead of the process account: %q", actual)
	}
	if uid := os.Geteuid(); uid >= 0 {
		id := strconv.Itoa(uid)
		if actual == "UID "+id {
			return
		}
		account, err := user.Lookup(actual)
		if err != nil || account.Uid != id {
			t.Fatalf("Docker check named an account that does not belong to the effective UID %s: %q", id, actual)
		}
	}
}

func TestDockerAccountWithNoLinuxPasswdEntryReportsUID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux uses the passwd file for static builds")
	}
	const uid = 1073741823
	passwd, err := os.ReadFile("/etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(passwd), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 3 && fields[2] == strconv.Itoa(uid) {
			t.Skip("test UID unexpectedly belongs to an account")
		}
	}
	t.Setenv("USER", "a-different-login-account")
	t.Setenv("USERNAME", "a-different-login-account")
	if actual := dockerAccountForUID(uid); actual != "UID 1073741823" {
		t.Fatalf("unresolved Docker account borrowed a shell name: %q", actual)
	}
}

func TestHostInfoDockerCheckCanRecoverWithoutRestartOrContainerCreation(t *testing.T) {
	_, _, statePath, read := containerUpdateFixture(t, "probe-error:permission denied while trying to connect to the Docker daemon socket")
	t.Setenv("DOCKER_HOST", "unix:///astral-test.sock")
	t.Setenv("DOCKER_CONTEXT", "")
	server := &Server{}
	blocked := server.HostInfo(context.Background())
	if blocked.ContainerAvailable || blocked.ContainerStatus != "permission_denied" || blocked.ContainerUser != dockerProcessUser() || !strings.Contains(blocked.ContainerError, "permission denied") {
		t.Fatalf("permission diagnosis omitted the actual account or original error: %+v", blocked)
	}
	state := read()
	state.Mode = ""
	if err := WriteJSON(statePath, state); err != nil {
		t.Fatal(err)
	}
	ready := server.HostInfo(context.Background())
	if !ready.ContainerAvailable || ready.ContainerStatus != "available" || ready.ContainerError != "" || ready.ContainerRuntime != "Test Docker" {
		t.Fatalf("fresh Docker check retained a stale failure: %+v", ready)
	}
	commands := read().Commands
	if len(commands) != 2 {
		t.Fatalf("expected a fresh probe on every check, got %v", commands)
	}
	for _, command := range commands {
		if len(command) < 3 || command[2] != "version" {
			t.Fatalf("read-only Docker check performed a mutation: %v", command)
		}
	}
}

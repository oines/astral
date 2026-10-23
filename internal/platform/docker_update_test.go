package platform

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
)

type fakeContainerState struct {
	Identity, Version, Boot, Mode string
	Running                       bool
	Credentials                   computers.Credentials
	Commands                      [][]string
	RestoreMode                   int64
}

// TestFakeDockerProcess is invoked in a child process behind a temporary Docker
// CLI shim. It models persistent container identity and mutation outcomes; it
// never starts Docker or touches the user's real container runtime.
func TestFakeDockerProcess(t *testing.T) {
	if os.Getenv("ASTRAL_FAKE_DOCKER") != "1" {
		return
	}
	path := os.Getenv("ASTRAL_FAKE_DOCKER_STATE")
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var state fakeContainerState
	if json.Unmarshal(raw, &state) != nil {
		os.Exit(2)
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	args = args[1:]
	state.Commands = append(state.Commands, append([]string{}, args...))
	if len(args) < 3 || args[0] != "--host" || args[1] != "unix:///astral-test.sock" {
		fmt.Fprintln(os.Stderr, "unfixed Docker target")
		os.Exit(2)
	}
	args = args[2:]
	output := ""
	code := 0
	switch args[0] {
	case "inspect":
		output = fmt.Sprintf(`{"ID":"original-container","Config":{"Labels":{"astral.operation":%q}},"State":{"Running":true}}`, state.Identity)
	case "exec":
		if args[1] != "original-container" {
			code = 2
			break
		}
		command := args[2:]
		switch command[0] {
		case "cat":
			value, _ := json.Marshal(state.Credentials)
			output = string(value)
		case "mkdir", "rm", "rmdir":
		case "/bin/sh":
			if strings.Contains(strings.Join(command, " "), "astral-replace") {
				state.Version = "v2"
				if strings.Contains(state.Mode, "lost-response") {
					code = 1
					output = "simulated response lost after atomic mv"
				}
			}
		default:
			if strings.HasPrefix(command[0], "/usr/local/bin/.astral-update-") {
				output = "v2"
			} else if command[0] == "/usr/local/bin/astral" {
				output = state.Version
			} else {
				code = 2
			}
		}
	case "cp":
		if args[1] == "original-container:/usr/local/bin/astral" {
			if os.WriteFile(args[2], []byte("previous-program"), 0600) != nil {
				code = 2
			}
		}
		if args[1] == "-" {
			archive := tar.NewReader(os.Stdin)
			header, readErr := archive.Next()
			if readErr != nil || header.Name != "astral" {
				code = 2
				break
			}
			state.RestoreMode = header.Mode
			data, _ := io.ReadAll(archive)
			if string(data) != "previous-program" || header.Mode != 0755 {
				code = 2
				break
			}
			if strings.Contains(state.Mode, "restore-fails") {
				code = 1
				output = "restore unavailable"
			} else {
				state.Version = "v1"
			}
		}
	case "restart":
		state.Running = true
		state.Boot = "new-boot"
	case "stop":
		state.Running = false
	case "start":
		state.Running = true
		state.Boot = "restored-boot"
	default:
		code = 2
		output = "unexpected container mutation"
	}
	raw, _ = json.Marshal(state)
	if os.WriteFile(path, raw, 0600) != nil {
		os.Exit(2)
	}
	if code == 0 {
		fmt.Fprint(os.Stdout, output)
	} else {
		fmt.Fprint(os.Stderr, output)
	}
	os.Exit(code)
}

func containerUpdateFixture(t *testing.T, mode string) (context.Context, string, string, func() fakeContainerState) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("subprocess shim uses a POSIX shell; endpoint and restore-tar tests run natively on Windows CI")
	}
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	read := func() fakeContainerState {
		raw, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		var state fakeContainerState
		if json.Unmarshal(raw, &state) != nil {
			t.Fatal("bad fake Docker state")
		}
		return state
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer enrolled-token" {
			http.Error(w, "invalid token", http.StatusForbidden)
			return
		}
		state := read()
		_ = json.NewEncoder(w).Encode(domain.ComputerStatus{Computer: domain.Computer{Meta: domain.Meta{ID: "computer-id"}, DaemonVersion: state.Version}, Online: state.Running, BootID: state.Boot})
	}))
	t.Cleanup(server.Close)
	state := fakeContainerState{Identity: "installation", Version: "v1", Boot: "original-boot", Mode: mode, Running: true, Credentials: computers.Credentials{ServerURL: server.URL, ComputerID: "computer-id", Token: "enrolled-token"}}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quotedExecutable := "'" + strings.ReplaceAll(executable, "'", "'\\''") + "'"
	shim := "#!/bin/sh\nexec " + quotedExecutable + " -test.run '^TestFakeDockerProcess$' -- \"$@\"\n"
	if err = os.WriteFile(filepath.Join(dir, "docker"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ASTRAL_FAKE_DOCKER", "1")
	t.Setenv("ASTRAL_FAKE_DOCKER_STATE", statePath)
	t.Setenv("DOCKER_HOST", "tcp://remote.invalid:2375")
	t.Setenv("DOCKER_CONTEXT", "remote")
	binary := filepath.Join(dir, "new-astral")
	if err = os.WriteFile(binary, []byte("new-verified-program"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return dockerContext(ctx, DockerInfo{Endpoint: "unix:///astral-test.sock", Arch: "amd64"}), binary, statePath, read
}

func assertOriginalContainerOnly(t *testing.T, state fakeContainerState) {
	t.Helper()
	for _, args := range state.Commands {
		if len(args) < 3 {
			t.Fatal(args)
		}
		args = args[2:]
		for _, forbidden := range []string{"create", "run", "build", "volume", "container", "rm"} {
			if args[0] == forbidden {
				t.Fatalf("update performed forbidden container recreation: %v", args)
			}
		}
		if args[0] == "exec" && args[1] != "original-container" {
			t.Fatalf("update touched another container: %v", args)
		}
	}
}

func TestContainerUpdateRetainsIdentityAndVerifiesNewBoot(t *testing.T) {
	ctx, binary, _, read := containerUpdateFixture(t, "")
	if err := UpdateContainerDaemon(ctx, "installation", binary, "v2", nil); err != nil {
		t.Fatal(err)
	}
	state := read()
	if state.Version != "v2" || state.Boot != "new-boot" || !state.Running {
		t.Fatalf("new Daemon did not reconnect: %+v", state)
	}
	assertOriginalContainerOnly(t, state)
}

func TestContainerUpdateLostMutationResponseRollsBackWithExecutableMode(t *testing.T) {
	ctx, binary, _, read := containerUpdateFixture(t, "lost-response")
	err := UpdateContainerDaemon(ctx, "installation", binary, "v2", nil)
	if err == nil || !strings.Contains(err.Error(), "previous program restored") {
		t.Fatalf("uncertain mutation was not rolled back: %v", err)
	}
	state := read()
	if state.Version != "v1" || !state.Running || state.RestoreMode != 0755 {
		t.Fatalf("old Daemon was not restored executable: %+v", state)
	}
	assertOriginalContainerOnly(t, state)
}

func TestContainerFailedRestoreKeepsRecoveryMaterial(t *testing.T) {
	ctx, binary, _, read := containerUpdateFixture(t, "lost-response restore-fails")
	err := UpdateContainerDaemon(ctx, "installation", binary, "v2", nil)
	if err == nil || !strings.Contains(err.Error(), "previous program retained at host") {
		t.Fatalf("missing recovery instructions: %v", err)
	}
	state := read()
	assertOriginalContainerOnly(t, state)
	backupPath := ""
	for _, args := range state.Commands {
		if len(args) > 4 && args[2] == "cp" && args[3] == "original-container:/usr/local/bin/astral" {
			backupPath = args[4]
		}
	}
	if backupPath == "" {
		t.Fatal("old program was never backed up")
	}
	defer os.RemoveAll(filepath.Dir(backupPath))
	if raw, readErr := os.ReadFile(backupPath); readErr != nil || string(raw) != "previous-program" {
		t.Fatalf("failed restore discarded manual recovery backup: %v", readErr)
	}
	for _, args := range state.Commands {
		if strings.Contains(strings.Join(args, " "), "rm -f") && strings.Contains(strings.Join(args, " "), ".astral-backup-") {
			t.Fatal("failed restore deleted in-container backup")
		}
	}
}

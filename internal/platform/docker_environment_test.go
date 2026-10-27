package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDockerEnvironmentPreservesSelectionsAndAddsInstallationDirectories(t *testing.T) {
	for _, test := range []struct {
		name, goos, executable, resolved, want string
		env                                    []string
	}{
		{
			name: "macOS GUI fallback and multicall installation", goos: "darwin",
			executable: "/usr/local/bin/docker", resolved: "/Applications/OrbStack.app/Contents/MacOS/xbin/docker-tools",
			env:  []string{"PATH=/chosen/helpers:/usr/bin:/bin", "DOCKER_CONFIG=/my docker/config", "HTTPS_PROXY=http://proxy.invalid"},
			want: "/chosen/helpers:/usr/bin:/bin:/usr/local/bin:/Applications/OrbStack.app/Contents/MacOS/xbin",
		},
		{
			name: "Linux custom CLI keeps explicit helper precedence", goos: "linux",
			executable: "/custom/bin/docker", resolved: "/opt/docker/bin/docker",
			env:  []string{"PATH=/custom/bin:/my/helpers:/usr/bin", "path=case-sensitive-other-variable"},
			want: "/custom/bin:/my/helpers:/usr/bin:/opt/docker/bin",
		},
		{
			name: "Windows PATH case and spaces", goos: "windows",
			executable: `D:\Selected Docker CLI\docker.exe`, resolved: `C:\Program Files\Docker\Docker\resources\bin\docker.exe`,
			env:  []string{"PATH=discarded-duplicate", `Path="c:/PROGRAM FILES/Docker/Docker/resources/bin";C:\Windows\System32`, "PATHEXT=.COM;.EXE;.BAT;.CMD"},
			want: `"c:/PROGRAM FILES/Docker/Docker/resources/bin";C:\Windows\System32;D:\Selected Docker CLI`,
		},
		{
			name: "Windows UNC installation", goos: "windows",
			executable: `\\server\share\Docker CLI\docker.exe`, resolved: `\\server\share\Docker CLI\docker.exe`,
			env: []string{`Path=C:\Windows\System32`}, want: `C:\Windows\System32;\\server\share\Docker CLI`,
		},
		{
			name: "Windows drive root", goos: "windows",
			executable: `D:\docker.exe`, resolved: `D:\docker.exe`,
			env: []string{`Path=C:\Windows\System32`}, want: `C:\Windows\System32;D:\`,
		},
		{
			name: "missing PATH", goos: "linux", executable: "/opt/docker/bin/docker", resolved: "/opt/docker/bin/docker",
			want: "/opt/docker/bin",
		},
		{
			name: "invalid lookup adds no relative directory", goos: "linux", executable: "docker", resolved: "docker",
			env: []string{"PATH=/usr/bin"}, want: "/usr/bin",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := append([]string{}, test.env...)
			got := dockerEnvironment(test.goos, test.env, test.executable, func(string) (string, error) { return test.resolved, nil })
			if value := dockerEnvironmentValue(test.goos, got, "PATH"); value != test.want {
				t.Fatalf("Docker child PATH = %q, want %q", value, test.want)
			}
			paths := 0
			for _, entry := range got {
				key, _, _ := strings.Cut(entry, "=")
				if dockerEnvironmentKey(test.goos, key, "PATH") {
					paths++
				}
			}
			if paths != 1 || !reflect.DeepEqual(before, append([]string{}, test.env...)) {
				t.Fatalf("duplicate PATH or mutated parent environment: %d, %q", paths, test.env)
			}
			for _, name := range []string{"DOCKER_CONFIG", "HTTPS_PROXY", "PATHEXT", "path"} {
				if dockerEnvironmentKey(test.goos, name, "PATH") {
					continue
				}
				if dockerEnvironmentValue(test.goos, got, name) != dockerEnvironmentValue(test.goos, test.env, name) {
					t.Fatalf("unrelated %s changed", name)
				}
			}
		})
	}
}

func TestDockerEnvironmentUnresolvedSymlinkStillAddsAliasDirectory(t *testing.T) {
	got := dockerEnvironment("linux", []string{"PATH=/usr/bin"}, "/custom/cli/docker", func(string) (string, error) { return "", os.ErrNotExist })
	if value := dockerEnvironmentValue("linux", got, "PATH"); value != "/usr/bin:/custom/cli" {
		t.Fatalf("selected CLI directory was lost: %q", value)
	}
}

func TestFixedDockerEnvironmentRemovesOnlyRoutingOverrides(t *testing.T) {
	var env []string
	for _, key := range []string{"DOCKER_CONTEXT", "DOCKER_HOST", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "BUILDX_BUILDER", "BUILDKIT_HOST"} {
		env = append(env, key+"=conflicting-target")
	}
	keep := []string{"Path=custom-tools", "DOCKER_CONFIG=custom-config", "HOME=user-home", "HTTP_PROXY=custom-proxy", "BUILDKIT_PROGRESS=plain", "PATHEXT=.EXE;.CMD"}
	env = append(env, keep...)
	if got := fixedDockerEnvironment(env); !reflect.DeepEqual(got, keep) {
		t.Fatalf("fixed Docker routing changed unrelated settings: %q", got)
	}
}

type dockerEnvironmentObservation struct {
	Executable string
	Args       []string
	Helper     string
	Config     string
	Path       string
	Overrides  []string
}

// Native executables keep this regression usable on Windows too: cmd/sh
// wrappers would add their own PATH and quoting behavior before Docker runs.
func TestMain(m *testing.M) {
	if os.Getenv("ASTRAL_DOCKER_ENVIRONMENT_FIXTURE") == "1" {
		os.Exit(runDockerEnvironmentFixture())
	}
	os.Exit(m.Run())
}

func runDockerEnvironmentFixture() int {
	if strings.HasPrefix(filepath.Base(os.Args[0]), "docker-credential-") {
		helper, err := exec.LookPath(os.Args[0])
		if err != nil {
			return 2
		}
		fmt.Fprint(os.Stdout, filepath.Dir(helper))
		return 0
	}
	if name := filepath.Base(os.Args[0]); name != "docker" && name != "docker.exe" {
		fmt.Fprintln(os.Stderr, "Docker multicall alias was not preserved")
		return 2
	}
	args := os.Args[1:]
	if file := os.Getenv("ASTRAL_DOCKER_ENVIRONMENT_LOG"); file != "" {
		raw, err := json.Marshal(args)
		if err != nil {
			return 2
		}
		log, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return 2
		}
		_, err = log.Write(append(raw, '\n'))
		if errors.Join(err, log.Close()) != nil {
			return 2
		}
	}
	if len(args) > 1 && args[0] == "--host" {
		args = args[2:]
	}
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "context":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"Name": "fixture-context", "Endpoints": map[string]any{"docker": map[string]string{"Host": "unix:///astral-cli-fixture.sock"}}})
	case "version":
		fmt.Fprint(os.Stdout, `{"Platform":{"Name":"Fixture Docker"},"KernelVersion":"fixture-kernel","Os":"linux","Arch":"amd64"}`)
	case "helper":
		helper, err := exec.Command("docker-credential-astral-test", "get").CombinedOutput()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Docker credential helper unavailable:", err)
			return 2
		}
		observation := dockerEnvironmentObservation{Executable: os.Args[0], Args: os.Args[1:], Helper: string(helper), Config: os.Getenv("DOCKER_CONFIG"), Path: os.Getenv("PATH")}
		for _, key := range []string{"DOCKER_CONTEXT", "DOCKER_HOST", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "BUILDX_BUILDER", "BUILDKIT_HOST"} {
			if value := os.Getenv(key); value != "" {
				observation.Overrides = append(observation.Overrides, key)
			}
		}
		if json.NewEncoder(os.Stdout).Encode(observation) != nil {
			return 2
		}
	default:
		return 2
	}
	return 0
}

func dockerEnvironmentFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "selected runtime with spaces", "bin")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name, helperName := "docker", "docker-credential-astral-test"
	if runtime.GOOS == "windows" {
		name += ".exe"
		helperName += ".exe"
	}
	install := func(source, target string) {
		t.Helper()
		// Windows cannot delete a hard link to the running parent test image,
		// even after the fixture children have exited. Each fixture executable
		// needs its own file; Unix can keep the inexpensive hard-link path.
		if runtime.GOOS != "windows" {
			if err := os.Link(source, target); err == nil {
				return
			}
		}
		input, err := os.Open(source)
		if err != nil {
			t.Fatal(err)
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			_ = input.Close()
			t.Fatal(err)
		}
		_, err = io.Copy(output, input)
		if err = errors.Join(err, output.Close(), input.Close()); err != nil {
			t.Fatal(err)
		}
	}
	cli := filepath.Join(directory, name)
	install(executable, cli)
	install(executable, filepath.Join(directory, helperName))
	log := filepath.Join(root, "calls.jsonl")
	t.Setenv("ASTRAL_DOCKER_ENVIRONMENT_FIXTURE", "1")
	t.Setenv("ASTRAL_DOCKER_ENVIRONMENT_LOG", log)
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	return cli, directory, log
}

func TestDockerEnvironmentFixtureCleansUpAfterNativeChildrenExit(t *testing.T) {
	cli, directory, _ := dockerEnvironmentFixture(t)
	t.Setenv("PATH", directory)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx = dockerContext(ctx, DockerInfo{Endpoint: "unix:///astral-cli-fixture.sock"})
	if _, err := dockerOutput(ctx, "helper"); err != nil {
		t.Fatal(err)
	}
	helper := "docker-credential-astral-test"
	if runtime.GOOS == "windows" {
		helper += ".exe"
	}
	// The parent test image is still running. Waiting for both native children
	// must leave their independent fixture files immediately removable.
	for _, file := range []string{filepath.Join(directory, helper), cli} {
		if err := os.Remove(file); err != nil {
			t.Fatalf("fixture executable remains locked after child exit: %v", err)
		}
	}
}

func TestDockerGUIFallbackRunsCredentialHelperFromSelectedInstallation(t *testing.T) {
	cli, directory, _ := dockerEnvironmentFixture(t)
	if runtime.GOOS != "windows" {
		// OrbStack invokes a multicall binary through an alias in another
		// directory; the credential helper belongs to the resolved installation.
		real := filepath.Join(directory, "docker-tools")
		if err := os.Rename(cli, real); err != nil {
			t.Fatal(err)
		}
		aliasDirectory := filepath.Join(t.TempDir(), "cli aliases")
		if err := os.MkdirAll(aliasDirectory, 0700); err != nil {
			t.Fatal(err)
		}
		cli = filepath.Join(aliasDirectory, "docker")
		if err := os.Symlink(real, cli); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", filepath.Join(t.TempDir(), "GUI system bin"))
	selected, err := resolveDockerExecutable(exec.LookPath, []string{cli})
	if err != nil {
		t.Fatal(err)
	}
	execution := &dockerExecution{executable: selected, env: dockerEnvironment(runtime.GOOS, os.Environ(), selected, filepath.EvalSymlinks)}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx = dockerContext(ctx, DockerInfo{Endpoint: "unix:///astral-cli-fixture.sock", execution: execution})
	raw, err := dockerOutput(ctx, "helper")
	if err != nil {
		t.Fatalf("CLI was found but could not start its own credential helper: %v", err)
	}
	var observation dockerEnvironmentObservation
	if err = json.Unmarshal(raw, &observation); err != nil {
		t.Fatal(err)
	}
	helperDirectory, helperErr := filepath.EvalSymlinks(observation.Helper)
	wantDirectory, directoryErr := filepath.EvalSymlinks(directory)
	if observation.Executable != cli || helperErr != nil || directoryErr != nil || helperDirectory != wantDirectory {
		t.Fatalf("wrong CLI alias or helper installation: %+v", observation)
	}
}

func TestProbeDockerFreezesCLIEnvironmentAndEndpointAcrossOperation(t *testing.T) {
	cli, directory, _ := dockerEnvironmentFixture(t)
	initialPath := directory
	config := filepath.Join(t.TempDir(), "Docker config with spaces")
	t.Setenv("PATH", initialPath)
	t.Setenv("DOCKER_CONFIG", config)
	t.Setenv("DOCKER_CONTEXT", "chosen-context")
	t.Setenv("DOCKER_HOST", "tcp://ignored-because-context-wins.invalid:2375")
	for _, key := range []string{"DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "BUILDX_BUILDER", "BUILDKIT_HOST"} {
		t.Setenv(key, "routing-override")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	probe, err := ProbeDocker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Endpoint != "unix:///astral-cli-fixture.sock" || probe.Platform != "Fixture Docker" || probe.KernelVersion != "fixture-kernel" {
		t.Fatalf("unexpected verified Docker target: %+v", probe)
	}
	wantPath := dockerEnvironmentValue(runtime.GOOS, probe.execution.env, "PATH")
	t.Setenv("PATH", filepath.Join(t.TempDir(), "unrelated runtime"))
	t.Setenv("DOCKER_CONFIG", "new config must not leak into an active operation")
	t.Setenv("DOCKER_CONTEXT", "new-context")
	t.Setenv("DOCKER_HOST", "tcp://new-host.invalid:2375")
	raw, err := dockerOutput(dockerContext(ctx, probe), "helper")
	if err != nil {
		t.Fatal(err)
	}
	var observation dockerEnvironmentObservation
	if err = json.Unmarshal(raw, &observation); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"--host", probe.Endpoint, "helper"}
	if observation.Executable != cli || observation.Config != config || observation.Path != wantPath || observation.Helper != directory || len(observation.Overrides) != 0 || !reflect.DeepEqual(observation.Args, wantArgs) {
		t.Fatalf("operation drifted from the selected CLI/environment/endpoint: %+v", observation)
	}
	// Nested operation contexts may carry only endpoint metadata. They must
	// inherit the existing execution, while a new operation follows a changed
	// explicit PATH instead of using a process-global cached installation.
	nextCLI, nextDirectory, _ := dockerEnvironmentFixture(t)
	t.Setenv("PATH", nextDirectory)
	parent := dockerContext(ctx, probe)
	nested := dockerContext(parent, DockerInfo{Endpoint: probe.Endpoint})
	if command := dockerCommand(nested, "helper"); command.Path != cli {
		t.Fatalf("nested context selected a different CLI: %q", command.Path)
	}
	fresh := dockerContext(context.Background(), DockerInfo{Endpoint: probe.Endpoint})
	if command := dockerCommand(fresh, "helper"); command.Path != nextCLI {
		t.Fatalf("new operation did not follow its explicit PATH: %q", command.Path)
	}
}

func TestProbeDockerTargetSelectionPrecedence(t *testing.T) {
	_, directory, log := dockerEnvironmentFixture(t)
	t.Setenv("PATH", directory)
	for _, test := range []struct {
		name, selected, host, endpoint string
		inspect                        bool
	}{
		{name: "context overrides host", selected: "explicit-context", host: "tcp://ignored.invalid:2375", endpoint: "unix:///astral-cli-fixture.sock", inspect: true},
		{name: "host overrides default context", host: "unix:///explicit-host.sock", endpoint: "unix:///explicit-host.sock"},
		{name: "current context", endpoint: "unix:///astral-cli-fixture.sock", inspect: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DOCKER_CONTEXT", test.selected)
			t.Setenv("DOCKER_HOST", test.host)
			if err := os.WriteFile(log, nil, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			probe, err := ProbeDocker(ctx)
			if err != nil || probe.Endpoint != test.endpoint {
				t.Fatalf("wrong selected endpoint: %+v, %v", probe, err)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if (len(lines) == 2) != test.inspect {
				t.Fatalf("unexpected context inspection: %s", raw)
			}
			if test.inspect {
				var args []string
				if err = json.Unmarshal([]byte(lines[0]), &args); err != nil {
					t.Fatal(err)
				}
				want := []string{"context", "inspect"}
				if test.selected != "" {
					want = append(want, test.selected)
				}
				want = append(want, "--format", "{{json .}}")
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("context choice changed: %q, want %q", args, want)
				}
			}
			var versionArgs []string
			if err = json.Unmarshal([]byte(lines[len(lines)-1]), &versionArgs); err != nil || len(versionArgs) < 3 || versionArgs[0] != "--host" || versionArgs[1] != test.endpoint || versionArgs[2] != "version" {
				t.Fatalf("version was not pinned to selected endpoint: %q, %v", versionArgs, err)
			}
		})
	}
}

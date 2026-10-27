package platform

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
)

type provisioningDockerState struct {
	Identity       string
	ContainerID    string
	Volume         string
	EnrollmentFile string
	MountedFile    string
	Commands       [][]string
	Starts         int
	ContainerState string
	ImageAvailable bool
	InjectedMode   int64
	FailInjection  bool
	ProbeNetwork   string
	ProbeURLs      []string
}

// The subprocess records Docker operations without invoking Docker or building
// an image. A hard link models a running container's file bind mount: atomic
// replacement of the host path only becomes visible after a remount/restart.
func TestProvisioningDockerProcess(t *testing.T) {
	path := os.Getenv("ASTRAL_PROVISION_TEST_STATE")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var state provisioningDockerState
	if json.Unmarshal(raw, &state) != nil {
		os.Exit(2)
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 4 {
		os.Exit(2)
	}
	args = args[1:]
	if args[0] != "--host" || args[1] != "unix:///astral-provision-test.sock" {
		os.Exit(2)
	}
	args = args[2:]
	state.Commands = append(state.Commands, append([]string{}, args...))
	output := ""
	exitCode := 0
	switch args[0] {
	case "version":
		output = `{"Platform":{"Name":"Test Docker"},"Os":"linux","Arch":"amd64"}`
	case "container":
		output = state.ContainerID
	case "inspect":
		if args[2] == "{{.State.Status}}" {
			output = state.ContainerState
		} else {
			output = state.Identity
		}
	case "image":
		if state.ImageAvailable {
			output = `{"Os":"linux","Architecture":"amd64"}`
		} else {
			exitCode = 1
		}
	case "pull":
		state.ImageAvailable = true
	case "rm":
		if len(args) != 3 || !strings.HasPrefix(args[2], "astral-network-check-") {
			os.Exit(2)
		}
	case "cp":
		if len(args) != 3 || args[1] != "-" || args[2] != state.ContainerID+":/usr/local/bin/" && args[2] != "network-check-container:/usr/local/bin/" {
			os.Exit(2)
		}
		archive := tar.NewReader(os.Stdin)
		header, readErr := archive.Next()
		if readErr != nil || header.Name != "astral" || header.Size == 0 {
			os.Exit(2)
		}
		if _, readErr = io.Copy(io.Discard, archive); readErr != nil {
			os.Exit(2)
		}
		if args[2] == "network-check-container:/usr/local/bin/" {
			break
		}
		if state.FailInjection {
			state.FailInjection, exitCode = false, 1
		} else {
			state.InjectedMode = header.Mode
		}
	case "volume":
		switch args[1] {
		case "create":
			for i, arg := range args {
				if arg == "--label" && i+1 < len(args) {
					state.Identity = strings.TrimPrefix(args[i+1], "astral.operation=")
				}
			}
			state.Volume = args[len(args)-1]
			output = state.Volume
		case "inspect":
			output = state.Identity
		default:
			os.Exit(2)
		}
	case "create":
		probe := false
		for i, arg := range args {
			if arg == "--name" && strings.HasPrefix(args[i+1], "astral-network-check-") {
				probe = true
			}
			if arg == "--network" {
				state.ProbeNetwork = args[i+1]
			}
			if arg == "astral-network-check" {
				state.ProbeURLs = args[i+2:]
			}
		}
		if probe {
			output = "network-check-container"
			break
		}
		for i, arg := range args {
			if arg == "--label" && i+1 < len(args) {
				state.Identity = strings.TrimPrefix(args[i+1], "astral.operation=")
			}
			if arg == "--mount" && i+1 < len(args) {
				fields, parseErr := csv.NewReader(strings.NewReader(args[i+1])).Read()
				if parseErr != nil {
					os.Exit(2)
				}
				for _, field := range fields {
					if strings.HasPrefix(field, "src=") && strings.Contains(args[i+1], "dst=/run/astral-enrollment.json") {
						state.EnrollmentFile = strings.TrimPrefix(field, "src=")
					}
				}
			}
		}
		if state.EnrollmentFile == "" {
			os.Exit(2)
		}
		state.ContainerID = "original-container"
		state.ContainerState = "created"
		if err = os.Link(state.EnrollmentFile, state.MountedFile); err != nil {
			os.Exit(2)
		}
		output = state.ContainerID
	case "start", "restart":
		if len(args) == 3 && args[1] == "--attach" && args[2] == "network-check-container" {
			if state.ProbeNetwork == "host" && len(state.ProbeURLs) > 0 {
				output = state.ProbeURLs[0]
			} else {
				exitCode = 1
			}
			break
		}
		if len(args) != 2 || args[1] != state.ContainerID {
			os.Exit(2)
		}
		if args[0] == "restart" {
			_ = os.Remove(state.MountedFile)
			if err = os.Link(state.EnrollmentFile, state.MountedFile); err != nil {
				os.Exit(2)
			}
		}
		state.Starts++
		state.ContainerState = "running"
		output = state.ContainerID
	default:
		fmt.Fprintln(os.Stderr, "unexpected provisioning Docker operation")
		os.Exit(2)
	}
	raw, _ = json.Marshal(state)
	if os.WriteFile(path+".next", raw, 0600) != nil || os.Rename(path+".next", path) != nil {
		os.Exit(2)
	}
	fmt.Fprint(os.Stdout, output)
	os.Exit(exitCode)
}

func provisioningFixture(t *testing.T) (*Server, context.Context, string, func() provisioningDockerState) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker launcher uses a POSIX shell; no real Docker is used")
	}
	root := t.TempDir()
	statePath := filepath.Join(root, "docker-state.json")
	state := provisioningDockerState{MountedFile: filepath.Join(root, "mounted-enrollment.json")}
	if err := WriteJSON(statePath, state); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run '^TestProvisioningDockerProcess$' -- \"$@\"\n"
	bin := filepath.Join(root, "bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "docker"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_HOST", "unix:///astral-provision-test.sock")
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("ASTRAL_PROVISION_TEST_STATE", statePath)
	// Race-instrumented helper processes have no background work to drain.
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	distribution := filepath.Join(root, "distribution")
	if err = os.MkdirAll(distribution, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(distribution, "astral-linux-amd64"), []byte("small test executable; never executed"), 0700); err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Listen: "0.0.0.0:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers"), DistributionDirectory: distribution, NoLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = server.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(func() { cancel(); _ = server.Close(context.Background()) })
	read := func() provisioningDockerState {
		var value provisioningDockerState
		if err := ReadJSON(statePath, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	return server, ctx, statePath, read
}

func provisioningID(server *Server, intent string) string {
	sum := sha256.Sum256([]byte(server.Config.DataDirectory + "\x00" + intent))
	return "container_" + hex.EncodeToString(sum[:12])
}
func connectProvisioningDaemon(t *testing.T, ctx context.Context, server *Server, path string, credentials *computers.Credentials) *computers.Daemon {
	t.Helper()
	var options ConnectOptions
	if err := ReadJSON(path, &options); err != nil {
		t.Fatal(err)
	}
	if options.ServerPin != server.Hub.ServerPin() {
		t.Fatal("container enrollment omitted or changed the Server pin")
	}
	// The fixture Daemon runs on the host. Keep the container-only route in
	// the mounted enrollment, while reaching that same pinned listener here.
	serverURL := containerServerURLOnHost(options.ServerURL)
	daemon, err := computers.NewDaemon(t.TempDir(), computers.Credentials{ServerURL: serverURL, ServerPin: options.ServerPin})
	if err != nil {
		t.Fatal(err)
	}
	if credentials == nil {
		registered, err := computers.RegisterRemote(ctx, serverURL, options.ServerPin, options.EnrollmentID, options.Token, daemon.Hello)
		if err != nil {
			t.Fatal(err)
		}
		daemon.Credentials = registered
	} else {
		daemon.Credentials = *credentials
	}
	go func() { _ = daemon.Connect(ctx) }()
	t.Cleanup(func() { _ = daemon.Close(context.Background()) })
	return daemon
}

func awaitProvisioned(t *testing.T, ctx context.Context, server *Server, id string) domain.Provisioning {
	t.Helper()
	var operation domain.Provisioning
	await(t, ctx, func() bool {
		if server.Store.Get(ctx, "provisioning", id, &operation) != nil {
			return false
		}
		return operation.State == "connected" || operation.State == "failed"
	})
	if operation.State != "connected" {
		t.Fatalf("container provisioning failed: %s", operation.Error)
	}
	return operation
}

func TestServerContainerProvisioningIncludesPinnedIdentity(t *testing.T) {
	server, ctx, _, read := provisioningFixture(t)
	operation, err := server.CreateContainer(ctx, "fresh-pinned-container")
	if err != nil {
		t.Fatal(err)
	}
	await(t, ctx, func() bool { return read().Starts > 0 })
	state := read()
	daemon := connectProvisioningDaemon(t, ctx, server, state.MountedFile, nil)
	completed := awaitProvisioned(t, ctx, server, operation.ID)
	if completed.ContainerID != "original-container" || completed.ComputerID != daemon.Credentials.ComputerID {
		t.Fatal("provisioning did not connect its original container")
	}
	if state.Volume != "astral-"+operation.ID+"-data" {
		t.Fatal("unexpected container volume")
	}
	if state.InjectedMode != 0755 {
		t.Fatalf("Daemon executable permissions were not retained: %o", state.InjectedMode)
	}
}

func TestContainerRetryCompletesInterruptedProgramInjection(t *testing.T) {
	server, ctx, statePath, read := provisioningFixture(t)
	state := read()
	state.FailInjection = true
	if err := WriteJSON(statePath, state); err != nil {
		t.Fatal(err)
	}
	enrollment := filepath.Join(filepath.Dir(statePath), "enrollment.json")
	if err := WriteJSON(enrollment, ConnectOptions{ServerURL: "https://server.example.com", ServerPin: server.Hub.ServerPin()}); err != nil {
		t.Fatal(err)
	}
	options := DockerOptions{Identity: "injection-retry", Binary: filepath.Join(server.Config.DistributionDirectory, "astral-linux-amd64"), EnrollmentFile: enrollment}
	id, err := LaunchContainer(ctx, options, nil)
	if err == nil || id == "" || read().Starts != 0 {
		t.Fatalf("interrupted injection started the container: %q %v %+v", id, err, read())
	}
	again, err := LaunchContainer(ctx, options, nil)
	if err != nil || again != id {
		t.Fatalf("retry did not finish original container: %q %v", again, err)
	}
	state = read()
	creates, pulls := 0, 0
	for _, command := range state.Commands {
		switch command[0] {
		case "build":
			t.Fatal("client rebuilt the environment")
		case "create":
			creates++
		case "pull":
			pulls++
		}
	}
	if creates != 1 || pulls != 1 || state.InjectedMode != 0755 || state.Starts != 1 {
		t.Fatalf("retry recreated or lost the environment: %+v", state)
	}
	options.Binary = filepath.Join(filepath.Dir(statePath), "not-needed-for-an-existing-computer")
	if _, err := LaunchContainer(ctx, options, nil); err != nil {
		t.Fatal("existing computer required a new program or download:", err)
	}
}

func seedProvisioningRetry(t *testing.T, ctx context.Context, server *Server, statePath, intent string, registered bool) (domain.Provisioning, ConnectOptions, *computers.Credentials) {
	t.Helper()
	result, err := server.Hub.Enroll(ctx, server.URL, true, false)
	if err != nil {
		t.Fatal(err)
	}
	options := ConnectOptions{ServerURL: server.URL, ServerPin: result.ServerPin, EnrollmentID: result.Enrollment.ID, Token: result.Token}
	var credentials *computers.Credentials
	if registered {
		daemon, err := computers.NewDaemon(t.TempDir(), computers.Credentials{ServerURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		value, err := computers.RegisterRemote(ctx, server.URL, options.ServerPin, options.EnrollmentID, options.Token, daemon.Hello)
		if err != nil {
			t.Fatal(err)
		}
		credentials = &value
	}
	var enrollment domain.Enrollment
	if err = server.Store.Get(ctx, "enrollments", options.EnrollmentID, &enrollment); err != nil {
		t.Fatal(err)
	}
	enrollment.ExpiresAt = time.Now().Add(-time.Minute)
	if err = server.Store.Save(ctx, "enrollments", &enrollment.Meta, &enrollment); err != nil {
		t.Fatal(err)
	}
	operation := domain.Provisioning{Meta: domain.Meta{ID: provisioningID(server, intent)}, IntentID: intent, Kind: "container", State: "failed", EnrollmentID: options.EnrollmentID, ContainerID: "original-container"}
	if credentials != nil {
		operation.ComputerID = credentials.ComputerID
	}
	if err = server.Store.Save(ctx, "provisioning", &operation.Meta, &operation); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(server.Config.ComputerDirectory, "provisioning", operation.ID, "enrollment.json")
	if !registered {
		// Model the route selected for an already-created bridge container.
		options.ServerURL = strings.Replace(options.ServerURL, "127.0.0.1", "host.docker.internal", 1)
		if options.ServerURL == server.URL {
			t.Fatal("fixture did not select a container-only bridge route")
		}
	}
	if err = WriteJSON(path, options); err != nil {
		t.Fatal(err)
	}
	var state provisioningDockerState
	if err = ReadJSON(statePath, &state); err != nil {
		t.Fatal(err)
	}
	state.Identity, state.ContainerID, state.Volume, state.EnrollmentFile = operation.ID, operation.ContainerID, "astral-"+operation.ID+"-data", path
	state.ContainerState = "exited"
	if err = os.Link(path, state.MountedFile); err != nil {
		t.Fatal(err)
	}
	if err = WriteJSON(statePath, state); err != nil {
		t.Fatal(err)
	}
	return operation, options, credentials
}

func TestServerContainerExpiredRetryReplacesTokenAndRestartsSameEnvironment(t *testing.T) {
	server, ctx, statePath, read := provisioningFixture(t)
	previous, old, _ := seedProvisioningRetry(t, ctx, server, statePath, "expired-retry", false)
	marker := filepath.Join(filepath.Dir(statePath), "agent-installed-tool")
	if err := os.WriteFile(marker, []byte("installed environment retained"), 0700); err != nil {
		t.Fatal(err)
	}
	var original domain.Enrollment
	if err := server.Store.Get(ctx, "enrollments", old.EnrollmentID, &original); err != nil {
		t.Fatal(err)
	}
	operation, err := server.CreateContainer(ctx, previous.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	await(t, ctx, func() bool { return read().Starts > 0 })
	state := read()
	var current ConnectOptions
	if err = ReadJSON(state.MountedFile, &current); err != nil {
		t.Fatal(err)
	}
	if current.EnrollmentID == old.EnrollmentID || current.Token == old.Token {
		t.Fatal("retry reused the expired registration token or stale bind-mounted file")
	}
	if current.ServerURL != old.ServerURL {
		t.Fatalf("retry lost the existing container's reachable Server route: got %q, want %q", current.ServerURL, old.ServerURL)
	}
	daemon := connectProvisioningDaemon(t, ctx, server, state.MountedFile, nil)
	completed := awaitProvisioned(t, ctx, server, operation.ID)
	if completed.ID != previous.ID || completed.ContainerID != previous.ContainerID || completed.ComputerID != daemon.Credentials.ComputerID {
		t.Fatal("retry replaced container or operation identity")
	}
	if state.Volume != "astral-"+previous.ID+"-data" {
		t.Fatal("retry replaced the environment volume")
	}
	var expired domain.Enrollment
	if err = server.Store.Get(ctx, "enrollments", old.EnrollmentID, &expired); err != nil {
		t.Fatal(err)
	}
	if !expired.ExpiresAt.Equal(original.ExpiresAt) || expired.TokenHash != original.TokenHash || expired.State != "waiting" {
		t.Fatal("retry revived or modified the expired enrollment")
	}
	if _, err = computers.RegisterRemote(ctx, containerServerURLOnHost(old.ServerURL), old.ServerPin, old.EnrollmentID, old.Token, daemon.Hello); err == nil {
		t.Fatal("old expired command became usable")
	}
	restarted := false
	for _, command := range state.Commands {
		if command[0] == "restart" && command[1] == previous.ContainerID {
			restarted = true
		}
		if command[0] == "build" || command[0] == "create" || command[0] == "volume" || command[0] == "rm" || command[0] == "cp" || command[0] == "pull" {
			t.Fatalf("retry recreated the environment or replaced its existing Daemon: %v", command)
		}
	}
	if !restarted {
		t.Fatal("retry did not remount the replaced enrollment file")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "installed environment retained" {
		t.Fatal("retry modified the installed environment")
	}
}

func TestServerContainerRetryRefusesChangedDockerEndpoint(t *testing.T) {
	server, ctx, statePath, read := provisioningFixture(t)
	previous, _, _ := seedProvisioningRetry(t, ctx, server, statePath, "changed-endpoint", false)
	root := filepath.Join(server.Config.ComputerDirectory, "provisioning", previous.ID)
	if err := WriteJSON(filepath.Join(root, "container.json"), ContainerInstallation{
		ID: previous.ContainerID, Identity: previous.ID, Endpoint: "unix:///original-docker-engine.sock", Name: "astral-" + previous.ID,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(read().EnrollmentFile)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := server.CreateContainer(ctx, previous.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	await(t, ctx, func() bool {
		return server.Store.Get(ctx, "provisioning", operation.ID, &operation) == nil && operation.State == "failed"
	})
	if !strings.Contains(operation.Error, "select its original context") {
		t.Fatalf("retry did not explain the changed Docker endpoint: %s", operation.Error)
	}
	state := read()
	if state.ContainerID != previous.ContainerID || state.Starts != 0 {
		t.Fatal("retry changed or started the original container")
	}
	for _, command := range state.Commands {
		if command[0] != "version" {
			t.Fatalf("retry touched the newly selected engine after detecting the endpoint mismatch: %v", command)
		}
	}
	after, err := os.ReadFile(state.EnrollmentFile)
	if err != nil || string(before) != string(after) {
		t.Fatal("endpoint mismatch replaced the original enrollment")
	}
	records, err := server.Store.List(ctx, "enrollments")
	if err != nil || len(records) != 1 {
		t.Fatal("endpoint mismatch issued a new pairing")
	}
}

func TestServerContainerRegisteredRetryKeepsCredentialsAndConsumedEnrollment(t *testing.T) {
	server, ctx, statePath, read := provisioningFixture(t)
	previous, old, credentials := seedProvisioningRetry(t, ctx, server, statePath, "registered-retry", true)
	before, err := os.ReadFile(read().EnrollmentFile)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := server.CreateContainer(ctx, previous.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	await(t, ctx, func() bool { return read().Starts > 0 })
	connectProvisioningDaemon(t, ctx, server, read().MountedFile, credentials)
	completed := awaitProvisioned(t, ctx, server, operation.ID)
	if completed.ContainerID != previous.ContainerID || completed.ComputerID != credentials.ComputerID || completed.EnrollmentID != old.EnrollmentID {
		t.Fatal("registered retry changed existing identity")
	}
	after, err := os.ReadFile(read().EnrollmentFile)
	if err != nil || string(before) != string(after) {
		t.Fatal("registered retry rewrote credentials or the consumed enrollment")
	}
	var record domain.Enrollment
	if err = server.Store.Get(ctx, "enrollments", old.EnrollmentID, &record); err != nil {
		t.Fatal(err)
	}
	if record.TokenHash != "" || record.State != "registered" || record.ExpiresAt.After(time.Now()) {
		t.Fatal("registered retry revived a consumed enrollment")
	}
	records, err := server.Store.List(ctx, "enrollments")
	if err != nil || len(records) != 1 {
		t.Fatal("registered retry issued a new pairing")
	}
}

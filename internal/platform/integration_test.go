//go:build integration

package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
)

func distributionServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)
	binaries, err := filepath.Abs("../../dist/bin")
	if err != nil {
		t.Fatal(err)
	}
	binaryName := "astral-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	if _, err = os.Stat(filepath.Join(binaries, binaryName)); err != nil {
		t.Fatal("build the native Astral distribution binary before integration tests")
	}
	root := t.TempDir()
	s, err := New(Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers"), DistributionDirectory: binaries})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

func installerCommand(t *testing.T, ctx context.Context, s *Server, root string, container bool) (*exec.Cmd, string) {
	t.Helper()
	enrollment, err := s.Hub.Enroll(ctx, s.URL, container, false)
	if err != nil {
		t.Fatal(err)
	}
	script, certificate := installerReleaseFixture(t, root, s.Config.DistributionDirectory)
	command := nativeInstallerCommand(ctx, script, s.URL, enrollment.ServerPin, enrollment.Enrollment.ID, enrollment.Token, filepath.Join(root, "daemon"), container)
	command.Env = append(os.Environ(), "XDG_DATA_HOME="+filepath.Join(root, "installed"), "CURL_CA_BUNDLE="+certificate)
	if runtime.GOOS == "windows" {
		command.Env = append(command.Env, "LOCALAPPDATA="+filepath.Join(root, "installed"))
	}
	return command, enrollment.Enrollment.ID
}

func enrollmentComputer(t *testing.T, ctx context.Context, s *Server, id string) domain.Computer {
	t.Helper()
	var computer domain.Computer
	await(t, ctx, func() bool {
		var record domain.Enrollment
		return s.Store.Get(ctx, "enrollments", id, &record) == nil && record.ComputerID != "" &&
			s.Store.Get(ctx, "computers", record.ComputerID, &computer) == nil && s.Hub.Status(computer).Online
	})
	return computer
}

func TestDistributionInstallerRegistersActualDaemon(t *testing.T) {
	s, ctx := distributionServer(t)
	runCtx, stop := context.WithCancel(ctx)
	root := t.TempDir()
	command, enrollment := installerCommand(t, runCtx, s, root, false)
	// Keep process output in a file so diagnostics can safely read it while the
	// foreground installer is still writing, including under the race detector.
	log, err := os.Create(filepath.Join(root, "installer.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var processErr error
	go func() {
		processErr = command.Wait()
		close(done)
	}()
	t.Cleanup(func() { stop(); <-done })
	registeredCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var remote domain.Computer
	var record domain.Enrollment
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.Store.Get(registeredCtx, "enrollments", enrollment, &record) == nil && record.ComputerID != "" &&
			s.Store.Get(registeredCtx, "computers", record.ComputerID, &remote) == nil && s.Hub.Status(remote).Online {
			break
		}
		select {
		case <-done:
			output, _ := os.ReadFile(log.Name())
			t.Fatalf("native installer exited before its Daemon connected: %v (enrollment state %q)\n%s", processErr, record.State, output)
		case <-registeredCtx.Done():
			output, _ := os.ReadFile(log.Name())
			t.Fatalf("native installer did not connect its Daemon: %v (enrollment state %q)\n%s", registeredCtx.Err(), record.State, output)
		case <-ticker.C:
		}
	}
	local, err := s.UseLocal(ctx)
	if err != nil || remote.ID == local.ID || remote.Kind != "remote" {
		t.Fatal("the installed daemon did not retain its independent identity")
	}
}

func removeTestContainer(t *testing.T, identity string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		name := "astral-" + identity
		_, _ = dockerOutput(ctx, "rm", "-f", name)
		_, _ = dockerOutput(ctx, "volume", "rm", name+"-data")
	})
}

func TestContainerShortcutAndInstallerUseIndependentRealComputers(t *testing.T) {
	s, ctx := distributionServer(t)
	local, err := s.UseLocal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agent := domain.Agent{Meta: domain.Meta{ID: domain.ID("agent")}, Name: "Container integration", AllowedComputers: []string{local.ID}, DefaultComputerID: local.ID}
	if err = s.Store.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
		t.Fatal(err)
	}
	operation, err := s.CreateContainer(ctx, "integration-container")
	if err != nil {
		t.Fatal(err)
	}
	removeTestContainer(t, operation.ID)
	await(t, ctx, func() bool {
		return s.Store.Get(ctx, "provisioning", operation.ID, &operation) == nil && (operation.State == "connected" || operation.State == "failed")
	})
	if operation.State != "connected" {
		t.Fatalf("container did not connect: %s\n%s", operation.Error, strings.Join(operation.Log, "\n"))
	}
	var container domain.Computer
	if err = s.Store.Get(ctx, "computers", operation.ComputerID, &container); err != nil {
		t.Fatal(err)
	}
	checkContainerInfo(t, container, operation.ID)
	again, err := s.CreateContainer(ctx, "integration-container")
	if err != nil || again.ID != operation.ID || again.ComputerID != operation.ComputerID {
		t.Fatal("retry duplicated the provisioning operation or Computer")
	}
	containers, err := dockerOutput(ctx, "ps", "-a", "--filter", "label=astral.operation="+operation.ID, "--format", "{{.ID}}")
	if err != nil || len(strings.Fields(string(containers))) != 1 {
		t.Fatal("a retried request created more than one container")
	}
	if err = s.Store.Get(ctx, "agents", agent.ID, &agent); err != nil || len(agent.AllowedComputers) != 1 {
		t.Fatal("registration silently widened the Agent's allowed Computers")
	}
	agent.AllowedComputers = append(agent.AllowedComputers, operation.ComputerID)
	if err = s.Store.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: agent.ID, Source: "web", ComputerID: operation.ComputerID}
	if err = s.Store.Save(ctx, "sessions", &session.Meta, &session); err != nil {
		t.Fatal(err)
	}
	binding, err := s.Hub.Bind(ctx, agent.ID, session.ID, operation.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	invocation := core.Invocation{SessionID: session.ID, ExecutionID: domain.ID("execution"), Binding: computers.BindingJSON(binding)}
	backend := &computers.Backend{Hub: s.Hub}
	process, err := backend.StartProcess(ctx, invocation, standardtools.ProcessRequest{Command: `printf 'container filesystem\n' > integration.txt; uname -s; cat integration.txt`})
	if err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(process.Output())
	_ = process.Output().Close()
	if err != nil {
		t.Fatal(err)
	}
	if code, err := process.Wait(); err != nil || code != 0 || string(output) != "Linux\ncontainer filesystem\n" {
		t.Fatalf("container command failed: %s (exit %d, %v)", output, code, err)
	}
	fs, err := backend.FileSystem(ctx, invocation)
	if err != nil {
		t.Fatal(err)
	}
	file, err := fs.ResolvePath(ctx, "integration.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	content, err := fs.ReadFile(ctx, file.Path)
	if err != nil || string(content) != "container filesystem\n" {
		t.Fatal("the real daemon filesystem did not contain the command's output")
	}
	// The generated remote-container command uses the same release downloads,
	// but a distinct enrollment and container identity.
	command, enrollment := installerCommand(t, ctx, s, t.TempDir(), true)
	identity := sha256.Sum256([]byte(s.URL + "\x00" + enrollment))
	removeTestContainer(t, hex.EncodeToString(identity[:12]))
	var installed bytes.Buffer
	command.Stdout, command.Stderr = &installed, &installed
	if err = command.Run(); err != nil {
		t.Fatalf("container installer failed: %v\n%s", err, installed.String())
	}
	remote := enrollmentComputer(t, ctx, s, enrollment)
	checkContainerInfo(t, remote, hex.EncodeToString(identity[:12]))
	if remote.Kind != "container" || remote.ID == local.ID || remote.ID == operation.ComputerID {
		t.Fatal("remote container registration replaced an existing Computer")
	}
	if err = s.Store.Get(ctx, "agents", agent.ID, &agent); err != nil || len(agent.AllowedComputers) != 2 {
		t.Fatal("remote container registration changed Agent authorization")
	}
}

func checkContainerInfo(t *testing.T, computer domain.Computer, identity string) {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	info := computer.Container
	if computer.Root != "/var/lib/astral" || info == nil || info.Name != "astral-"+identity || info.Host != host || info.Volume != "astral-"+identity+"-data" || info.MountPath != "/var/lib/astral" || !strings.HasPrefix(info.Image, "astral-computer:") {
		t.Fatalf("container did not report its actual creator and storage: %+v, %+v", computer, info)
	}
}

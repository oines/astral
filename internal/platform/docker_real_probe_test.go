package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// This opt-in acceptance test only uses an already cached image ID. It never
// calls ensureComputerImage, pulls/builds an image, installs a service, or opens
// a real Astral data directory. The binary/image inputs are test-only knobs.
func TestRealDockerProbeFromMinimalGUIEnvironment(t *testing.T) {
	if os.Getenv("ASTRAL_REAL_DOCKER_PROBE") != "1" {
		t.Skip("set ASTRAL_REAL_DOCKER_PROBE=1 for disposable-container acceptance")
	}
	binary := os.Getenv("ASTRAL_REAL_DOCKER_BINARY")
	image := os.Getenv("ASTRAL_REAL_DOCKER_IMAGE_ID")
	if !filepath.IsAbs(binary) || !imageDigestPattern.MatchString(image) {
		t.Fatal("the test requires an absolute Linux binary path and an existing local sha256 image ID")
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatal("the test Linux binary is unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	configDirectory := os.Getenv("DOCKER_CONFIG")
	if configDirectory == "" {
		configDirectory = filepath.Join(home, ".docker")
	}
	configPath := filepath.Join(configDirectory, "config.json")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal("cannot read the existing Docker configuration for an integrity check")
	}
	configHash := sha256.Sum256(config)
	var credentials struct{ CredsStore string }
	if json.Unmarshal(config, &credentials) != nil || credentials.CredsStore != "osxkeychain" {
		t.Fatal("this acceptance fixture requires the existing osxkeychain credential store")
	}
	t.Cleanup(func() {
		after, e := os.ReadFile(configPath)
		if e != nil || sha256.Sum256(after) != configHash {
			t.Error("the original Docker configuration changed during acceptance")
		}
	})
	// Reproduce the GUI/SSH environment which can find Docker only through an
	// absolute fallback, and cannot find its credential helper through PATH.
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	if _, err = exec.LookPath("docker-credential-osxkeychain"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatal("the acceptance PATH unexpectedly already contains the credential helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	probe, err := ProbeDocker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ctx = dockerContext(ctx, probe)
	t.Logf("runtime=%s arch=%s context=%s endpoint=%s; minimal PATH lacks the helper", probe.Name, probe.Arch, probe.Context, probe.Endpoint)
	if probe.Arch != "arm64" {
		t.Fatal("this acceptance binary targets Linux arm64")
	}
	// A nonexistent, unique keychain entry cannot disclose a real registry
	// credential. env performs the lookup using the actual Docker child PATH.
	helperCtx, stopHelper := context.WithTimeout(ctx, 10*time.Second)
	helper := exec.CommandContext(helperCtx, "/usr/bin/env", "docker-credential-osxkeychain", "get")
	helper.Env = append([]string{}, probe.execution.env...)
	helperEntry := sha256.Sum256([]byte(t.TempDir()))
	helper.Stdin = strings.NewReader("https://astral-acceptance-" + hex.EncodeToString(helperEntry[:8]) + ".invalid")
	helperOutput, helperErr := helper.CombinedOutput()
	stopHelper()
	var helperExit *exec.ExitError
	if !errors.As(helperErr, &helperExit) || helperExit.ExitCode() != 1 || !strings.Contains(string(helperOutput), "credentials not found in native keychain") {
		t.Fatal("the real credential helper was not discovered or did not reject the nonexistent keychain entry; output withheld")
	}
	t.Log("the prepared Docker child PATH executed the real osxkeychain helper; nonexistent entry rejected, no credentials printed")
	var cached struct {
		ID, OS, Architecture string
		RepoTags             []string
	}
	raw, err := dockerOutput(ctx, "image", "inspect", "--format", "{{json .}}", image)
	if err != nil || json.Unmarshal(raw, &cached) != nil || cached.ID != image || cached.OS != "linux" || cached.Architecture != probe.Arch || !slices.Contains(cached.RepoTags, "ubuntu:24.04") {
		t.Fatal("the supplied local image is not the already cached Ubuntu 24.04 for this runtime")
	}
	// Both commands only retrieve a public registry manifest. The negative
	// control uses the original short PATH; the fixed command keeps the same
	// Docker executable, context and credential configuration.
	metadataCtx, stopMetadata := context.WithTimeout(ctx, 15*time.Second)
	broken := exec.CommandContext(metadataCtx, probe.execution.executable, "--host", probe.Endpoint, "manifest", "inspect", "docker.io/library/ubuntu:24.04")
	broken.Env = fixedDockerEnvironment(os.Environ())
	brokenOutput, brokenErr := broken.CombinedOutput()
	if brokenErr != nil && strings.Contains(string(brokenOutput), "docker-credential-osxkeychain") && strings.Contains(string(brokenOutput), "not found") {
		t.Log("short-PATH registry negative control reproduced the credential-helper lookup failure")
	} else {
		t.Logf("short-PATH registry negative control: %s; no image pull/build", realDockerMetadataResult(metadataCtx, brokenErr, brokenOutput))
	}
	stopMetadata()
	metadataCtx, stopMetadata = context.WithTimeout(ctx, 15*time.Second)
	manifest := dockerCommand(metadataCtx, "manifest", "inspect", "docker.io/library/ubuntu:24.04")
	manifestOutput, manifestErr := manifest.CombinedOutput()
	if manifestErr != nil {
		t.Logf("fixed-environment registry metadata: %s; continuing with the local cached image", realDockerMetadataResult(metadataCtx, manifestErr, manifestOutput))
	} else {
		var metadata struct{ SchemaVersion int }
		if json.Unmarshal(manifestOutput, &metadata) != nil || metadata.SchemaVersion != 2 {
			t.Fatal("the registry did not return a valid public manifest")
		}
		t.Log("fixed Docker environment retrieved the public manifest with the original credential store; no image pull/build")
	}
	stopMetadata()
	before := realDockerContainerIDs(t, ctx)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		cleanup = dockerContext(cleanup, probe)
		// The production probe removes only its own random container name.
		// Do not sweep a shared label: another user action may run concurrently.
		remaining := realDockerContainerIDs(t, cleanup)
		slices.Sort(before)
		slices.Sort(remaining)
		if !slices.Equal(before, remaining) {
			t.Error("container identities before and after acceptance differ")
		} else {
			t.Logf("cleanup verified: %d pre-existing container identities retained, no temporary containers remain", len(before))
		}
	})
	root := t.TempDir()
	server, err := NewContext(ctx, Config{Listen: "0.0.0.0:0", NoLocal: true, DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers"), Version: "0.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := server.Close(closeCtx); err != nil {
			t.Error("the isolated temporary Server did not stop cleanly")
		}
	})
	if err = server.Start(); err != nil {
		t.Fatal(err)
	}
	enrollment := ConnectOptions{ServerURL: server.URL, ServerPin: server.Hub.ServerPin()}
	network, address, err := containerRoute(ctx, image, probe.Arch, binary, enrollment, nil, func(line string) { t.Log(line) })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cached Ubuntu container verified the temporary Server pin: network=%s address=%s", network, address)
	if _, err = probeContainerURLs(ctx, image, probe.Arch, network, binary, strings.Repeat("0", 64), []string{address}); err == nil {
		t.Fatal("the real container accepted a wrong Server pin")
	}
	t.Log("the same real container route rejected a different Server pin")
}

func realDockerMetadataResult(ctx context.Context, err error, output []byte) string {
	if err == nil {
		return "success"
	}
	if ctx.Err() != nil {
		return "metadata deadline exceeded (registry availability not verified)"
	}
	lower := strings.ToLower(string(output))
	var markers []string
	for _, marker := range []string{"timeout", "deadline", "tls", "x509", "unauthorized", "toomanyrequests", "no such host", "connection refused", "eof", "docker-credential-osxkeychain"} {
		if strings.Contains(lower, marker) {
			markers = append(markers, marker)
		}
	}
	if len(markers) != 0 {
		return "error categories=" + strings.Join(markers, ",") + " (body withheld)"
	}
	return "metadata request failed (body withheld)"
}

func realDockerContainerIDs(t *testing.T, ctx context.Context) []string {
	t.Helper()
	raw, err := dockerOutput(ctx, "container", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		t.Fatal("cannot snapshot local container identities for cleanup verification")
	}
	return strings.Fields(string(raw))
}

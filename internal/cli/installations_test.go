package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/platform"
)

func installationEnvironment(t *testing.T) (config, data string) {
	t.Helper()
	root := t.TempDir()
	config, data = filepath.Join(root, "config"), filepath.Join(root, "data")
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("ASTRAL_SERVER_DATA_DIR", "")
	t.Setenv("ASTRAL_INSTALLATION_ROOT", "")
	t.Setenv("ASTRAL_DESKTOP_DATA_DIR", "")
	t.Setenv("ASTRAL_ADMIN_USERNAME", "")
	t.Setenv("ASTRAL_ADMIN_PASSWORD", "")
	t.Setenv("ASTRAL_ADMIN_PASSWORD_FILE", "")
	return config, data
}

func defaultServerInstallation(t *testing.T, dataHome string) string {
	t.Helper()
	root := filepath.Join(dataHome, "astral-server")
	directory := filepath.Join(root, "data", "server")
	if err := platform.WriteJSON(filepath.Join(directory, "network.json"), map[string]any{"restrict_hostnames": true, "allowed_hostnames": []string{"unreachable.example"}}); err != nil {
		t.Fatal(err)
	}
	if err := platform.WriteJSON(filepath.Join(root, "installation.json"), map[string]any{"schema": 1, "root": root, "data_directory": directory, "uid": os.Getuid(), "service": "astral-server.service"}); err != nil {
		t.Fatal(err)
	}
	return directory
}

func installationTestContext(config, data string) context.Context {
	return context.WithValue(context.Background(), installationDirectoriesKey{}, installationDirectories{configRoot: filepath.Join(config, "astral"), dataHome: data})
}

func TestServerCommandsFindDefaultInstallationAndRespectExplicitData(t *testing.T) {
	config, dataHome := installationEnvironment(t)
	ctx := installationTestContext(config, dataHome)
	want := defaultServerInstallation(t, dataHome)
	if got, err := resolveServerData(ctx, "", io.Discard, false); err != nil || got != want {
		t.Fatalf("default installation not selected: %q %v", got, err)
	}
	override := filepath.Join(t.TempDir(), "environment-data")
	t.Setenv("ASTRAL_SERVER_DATA_DIR", override)
	if got, err := resolveServerData(ctx, "", io.Discard, false); err != nil || got != override {
		t.Fatalf("environment override lost priority: %q %v", got, err)
	}
	explicit := filepath.Join(t.TempDir(), "explicit-data")
	if got, err := resolveServerData(ctx, explicit, io.Discard, false); err != nil || got != explicit {
		t.Fatalf("explicit --data lost priority: %q %v", got, err)
	}
}

func TestUnattendedAccountSetupStatusAndRecoveryUseInstalledServer(t *testing.T) {
	config, dataHome := installationEnvironment(t)
	ctx := installationTestContext(config, dataHome)
	directory := defaultServerInstallation(t, dataHome)
	passwordFile := filepath.Join(t.TempDir(), "administrator-password")
	if err := os.WriteFile(passwordFile, []byte("private-test-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASTRAL_ADMIN_USERNAME", "owner")
	t.Setenv("ASTRAL_ADMIN_PASSWORD_FILE", passwordFile)
	var out, stderr strings.Builder
	if code := Run(ctx, []string{"auth", "setup"}, &out, &stderr); code != 0 {
		t.Fatalf("default-installation setup failed: %s", stderr.String())
	}
	if strings.Contains(out.String()+stderr.String(), "private-test-password") {
		t.Fatal("initialization printed the password")
	}
	out.Reset()
	stderr.Reset()
	if code := Run(ctx, []string{"auth", "status"}, &out, &stderr); code != 0 {
		t.Fatalf("default-installation status failed: %s", stderr.String())
	}
	var status auth.Status
	if err := json.Unmarshal([]byte(out.String()), &status); err != nil || !status.Configured {
		t.Fatalf("status not machine-readable/configured: %s %v", out.String(), err)
	}
	out.Reset()
	stderr.Reset()
	if code := Run(ctx, []string{"config", "hostnames", "disable", "--json"}, &out, &stderr); code != 0 {
		t.Fatalf("default-installation recovery failed: %s", stderr.String())
	}
	var settings struct {
		DataDirectory     string `json:"data_directory"`
		RestrictHostnames struct {
			Value bool `json:"value"`
		} `json:"restrict_hostnames"`
		AllowedHostnames struct {
			Value []string `json:"value"`
		} `json:"allowed_hostnames"`
	}
	if err := json.Unmarshal([]byte(out.String()), &settings); err != nil || settings.DataDirectory != directory || settings.RestrictHostnames.Value || len(settings.AllowedHostnames.Value) != 1 || settings.AllowedHostnames.Value[0] != "unreachable.example" {
		t.Fatalf("recovery did not disable the installed Server's restriction while retaining its list: %s %v", out.String(), err)
	}
}

func TestMultipleInstallationsRequireAChoiceWithoutATerminal(t *testing.T) {
	if index, err := chooseInstallation("Server", "--data DIRECTORY", []string{"one"}, strings.NewReader(""), io.Discard, false); err != nil || index != 0 {
		t.Fatalf("single installation still requires an argument: %d %v", index, err)
	}
	if _, err := chooseInstallation("Server", "--data DIRECTORY", []string{"one", "two"}, strings.NewReader(""), io.Discard, false); err == nil || !strings.Contains(err.Error(), "--data") {
		t.Fatal("noninteractive ambiguous installation was silently chosen")
	}
	if index, err := chooseInstallation("Server", "--data DIRECTORY", []string{"one", "two"}, strings.NewReader("2\n"), io.Discard, true); err != nil || index != 1 {
		t.Fatalf("interactive installation choice failed: %d %v", index, err)
	}
}

func TestComputerCommandsFindManagedDefaultInstallation(t *testing.T) {
	config, dataHome := installationEnvironment(t)
	ctx := installationTestContext(config, dataHome)
	root := filepath.Join(config, "astral")
	directory := filepath.Join(root, "computers", "0123456789ab", "enrollment-test")
	if err := platform.WriteJSON(filepath.Join(directory, "credentials.json"), map[string]string{"computer_id": "computer-test", "server_url": "https://astral.example"}); err != nil {
		t.Fatal(err)
	}
	if err := platform.WriteJSON(filepath.Join(directory, "service", "spec.json"), map[string]string{"executable": "test-astral"}); err != nil {
		t.Fatal(err)
	}
	installation, err := resolveComputerInstallation(ctx, false, io.Discard)
	canonical, canonicalErr := filepath.EvalSymlinks(directory)
	if err != nil || canonicalErr != nil || installation.Root != canonical || installation.ComputerID != "computer-test" {
		t.Fatalf("default Computer installation not found: %+v %v", installation, err)
	}
	if err = os.Remove(filepath.Join(directory, "service", "spec.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = resolveComputerInstallation(ctx, false, io.Discard); err == nil {
		t.Fatal("uninstalled Daemon was offered as a managed service")
	}
}

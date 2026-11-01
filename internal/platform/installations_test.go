package platform

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oines/astral/internal/computers"
)

func TestComputerDiscoveryFindsDefaultAndIndexedInstallationsWithoutExposingCredentials(t *testing.T) {
	configuration := t.TempDir()
	defaultComputer := filepath.Join(configuration, "computers", "server-one", "enrollment-one")
	customComputer := filepath.Join(t.TempDir(), "custom-computer")
	for _, root := range []string{defaultComputer, customComputer} {
		if err := WriteJSON(filepath.Join(root, "credentials.json"), computers.Credentials{ComputerID: "computer-one", ServerURL: "https://astral.example"}); err != nil {
			t.Fatal(err)
		}
		if err := WriteJSON(filepath.Join(root, "service", "spec.json"), map[string]string{"executable": "fixture-astral"}); err != nil {
			t.Fatal(err)
		}
	}
	for name, root := range map[string]string{"custom": customComputer, "duplicate": defaultComputer, "removed": filepath.Join(t.TempDir(), "does-not-exist")} {
		if err := WriteJSON(filepath.Join(configuration, "installations", "computers", name+".json"), ComputerInstallation{Root: root}); err != nil {
			t.Fatal(err)
		}
	}
	containerRoot := filepath.Join(configuration, "computers", "server-two", "enrollment-two")
	if err := WriteJSON(filepath.Join(containerRoot, "container.json"), ContainerInstallation{Name: "astral-container-one", Identity: "container-one", Context: "original-context", Endpoint: "unix:///original.sock"}); err != nil {
		t.Fatal(err)
	}
	values, err := InstalledComputers(configuration)
	if err != nil || len(values) != 3 {
		t.Fatalf("known installations not discovered/deduplicated: %+v %v", values, err)
	}
	containerFound := false
	for _, value := range values {
		if !value.Managed || !filepath.IsAbs(value.Root) {
			t.Fatalf("invalid installation offered: %+v", value)
		}
		if value.Container != "" {
			containerFound = value.DockerContext == "original-context" && value.DockerEndpoint == "unix:///original.sock"
		}
	}
	if !containerFound {
		t.Fatal("container discovery lost its original Docker target")
	}
}

func TestAutomaticallySelectedContainerCannotUpdateAnotherDockerEndpoint(t *testing.T) {
	ctx := dockerContext(context.Background(), DockerInfo{Endpoint: "unix:///different.sock", Arch: "amd64"})
	err := UpdateComputer(ctx, UpdateOptions{Container: "astral-container-one", ContainerEndpoint: "unix:///original.sock", Version: "0.1.9"}, nil)
	if err == nil || !strings.Contains(err.Error(), "original Docker context") {
		t.Fatalf("automatic selection changed Docker target before updating: %v", err)
	}
}

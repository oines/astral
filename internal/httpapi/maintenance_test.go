package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestMaintenanceCommandsPreserveIdentityWithoutCredentials(t *testing.T) {
	computer := domain.Computer{Kind: "remote", OS: "windows", Root: `C:\Users\Alice's Work\Astral`, TokenID: "secret-credential-id", TokenHash: "private-token-hash"}
	value, err := maintenanceCommands("https://astral.example.com", computer)
	if err != nil {
		t.Fatal(err)
	}
	if value.Manager != "task-scheduler" || !strings.Contains(value.Command, "--root 'C:\\Users\\Alice'\\''s Work\\Astral'") || !strings.Contains(value.PowerShellCommand, "-Root 'C:\\Users\\Alice''s Work\\Astral'") {
		t.Fatalf("update commands lost the exact Computer directory: %+v", value)
	}
	for _, command := range []string{value.Command, value.PowerShellCommand} {
		if strings.Contains(command, computer.TokenID) || strings.Contains(command, computer.TokenHash) || strings.Contains(strings.ToLower(command), "token") || strings.Contains(command, "enrollment") {
			t.Fatal("maintenance commands must use existing local credentials")
		}
	}
	computer.Kind = "container"
	computer.Container = &domain.ContainerInfo{Name: "astral-container_123"}
	value, err = maintenanceCommands("https://astral.example.com", computer)
	if err != nil || value.Manager != "docker" || !strings.Contains(value.Command, "--container 'astral-container_123'") || !strings.Contains(value.PowerShellCommand, "-ContainerID 'astral-container_123'") || strings.Contains(value.Command, "--root") {
		t.Fatalf("container update must target its original container: %+v %v", value, err)
	}
}

func TestMaintenanceDoesNotInventMissingRemoteIdentity(t *testing.T) {
	for _, computer := range []domain.Computer{{Kind: "remote"}, {Kind: "container"}, {Kind: "remote", Revoked: true}} {
		if _, err := maintenanceCommands("https://astral.example.com", computer); err == nil {
			t.Fatalf("generated an update command without a usable Computer identity: %+v", computer)
		}
	}
	value, err := maintenanceCommands("", domain.Computer{Local: true})
	if err != nil || value.Manager != "desktop" || value.Command != "" || value.PowerShellCommand != "" {
		t.Fatalf("a local Daemon should upgrade with its Server: %+v %v", value, err)
	}
}

func TestMaintenanceEndpointRequiresOwnerAndUsesRecordedDirectory(t *testing.T) {
	p := testkit.New(t, 1, nil)
	computer := domain.Computer{Meta: domain.Meta{ID: "maintenance-remote"}, Kind: "remote", OS: "linux", Root: "/home/alice/Astral work"}
	if err := p.Store.Save(p.Context, "computers", &computer.Meta, &computer); err != nil {
		t.Fatal(err)
	}
	a, server := apiFixture(t, p)
	a.Config.PublicURL = "https://astral.example.com"
	response := request(t, p.Context, server, "GET", "/api/v1/computers/maintenance/maintenance-remote", nil, false)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("maintenance endpoint accepted an anonymous request")
	}
	response = request(t, p.Context, server, "GET", "/api/v1/computers/maintenance/maintenance-remote", nil, true)
	defer response.Body.Close()
	var value domain.ComputerMaintenance
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || value.Manager != "systemd-user" || !strings.Contains(value.Command, "--root '/home/alice/Astral work'") {
		t.Fatalf("maintenance endpoint did not use the recorded target: %d %+v", response.StatusCode, value)
	}
}

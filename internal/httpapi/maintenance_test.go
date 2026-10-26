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
	value, err := maintenanceCommands(computer)
	if err != nil {
		t.Fatal(err)
	}
	if value.Manager != "task-scheduler" || !strings.Contains(value.Command, "--root 'C:\\Users\\Alice'\\''s Work\\Astral'") || !strings.Contains(value.PowerShellCommand, "-Root 'C:\\Users\\Alice''s Work\\Astral'") {
		t.Fatalf("update commands lost the exact Computer directory: %+v", value)
	}
	for _, command := range []string{value.Command, value.PowerShellCommand} {
		if !strings.Contains(command, "https://github.com/oines/astral/releases/latest/download/install-computer.") || strings.Contains(command, "astral.example.com/install.") {
			t.Fatal("maintenance bootstrap must use the official HTTPS release")
		}
		if !strings.Contains(command, "--proto '=https' --proto-redir '=https'") {
			t.Fatal("maintenance bootstrap permits a TLS downgrade")
		}
	}
	if !strings.Contains(value.PowerShellCommand, "curl.exe") || strings.Contains(value.PowerShellCommand, "Invoke-WebRequest") || strings.Index(value.PowerShellCommand, "$LASTEXITCODE -ne 0") > strings.Index(value.PowerShellCommand, "[scriptblock]::Create") {
		t.Fatal("PowerShell bootstrap executes a failed or downgraded download")
	}
	for _, command := range []string{value.Command, value.PowerShellCommand} {
		if strings.Contains(command, "--server") || strings.Contains(command, "-Server") {
			t.Fatal("updates must use the locally stored Server address and pin")
		}
	}
	for _, command := range []string{value.Command, value.PowerShellCommand} {
		if strings.Contains(command, computer.TokenID) || strings.Contains(command, computer.TokenHash) || strings.Contains(strings.ToLower(command), "token") || strings.Contains(command, "enrollment") {
			t.Fatal("maintenance commands must use existing local credentials")
		}
	}
	computer.Kind = "container"
	computer.Container = &domain.ContainerInfo{Name: "astral-container_123"}
	value, err = maintenanceCommands(computer)
	if err != nil || value.Manager != "docker" || !strings.Contains(value.Command, "--container 'astral-container_123'") || !strings.Contains(value.PowerShellCommand, "-ContainerID 'astral-container_123'") || strings.Contains(value.Command, "--root") {
		t.Fatalf("container update must target its original container: %+v %v", value, err)
	}
}

func TestMaintenanceDoesNotInventMissingRemoteIdentity(t *testing.T) {
	for _, computer := range []domain.Computer{{Kind: "remote"}, {Kind: "container"}, {Kind: "remote", Revoked: true}} {
		if _, err := maintenanceCommands(computer); err == nil {
			t.Fatalf("generated an update command without a usable Computer identity: %+v", computer)
		}
	}
	value, err := maintenanceCommands(domain.Computer{Local: true})
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
	a.Config.PublicURL = "http://astral.example.com"
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
	if !strings.Contains(value.Command, "https://github.com/oines/astral/releases/latest/download/install-computer.sh") || strings.Contains(value.Command, "http://astral.example.com/install.sh") || !strings.Contains(value.PowerShellCommand, "install-computer.ps1") {
		t.Fatal("HTTP Server maintenance endpoint generated an untrusted script download")
	}
	if response.StatusCode != 200 || value.Manager != "systemd-user" || !strings.Contains(value.Command, "--root '/home/alice/Astral work'") {
		t.Fatalf("maintenance endpoint did not use the recorded target: %d %+v", response.StatusCode, value)
	}
}

func TestDaemonUpdateEndpointIsOwnerOnlyAndRejectsArbitrarySources(t *testing.T) {
	p := testkit.New(t, 1, nil)
	computer := domain.Computer{Meta: domain.Meta{ID: "daemon-update"}, Kind: "remote", OS: "linux", Root: "/home/alice/astral"}
	if err := p.Store.Save(p.Context, "computers", &computer.Meta, &computer); err != nil {
		t.Fatal(err)
	}
	_, server := apiFixture(t, p)
	for _, body := range []string{`{}`, `{"url":"https://attacker.invalid/astral","version":"999.0.0","sha256":"forged"}`} {
		response := request(t, p.Context, server, "POST", "/api/v1/computers/maintenance/daemon-update/update", strings.NewReader(body), false)
		response.Body.Close()
		if response.StatusCode != 401 {
			t.Fatalf("anonymous Daemon update accepted: %d", response.StatusCode)
		}
	}
	response := request(t, p.Context, server, "POST", "/api/v1/computers/maintenance/daemon-update/update", strings.NewReader(`{"url":"https://attacker.invalid/astral"}`), true)
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("update request accepted an arbitrary download source: %d", response.StatusCode)
	}
}

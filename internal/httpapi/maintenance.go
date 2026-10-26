package httpapi

import (
	"net/http"
	"strings"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/releasefeed"
)

func (a *API) registerComputerMaintenance() {
	route[httpcontract.Empty, domain.ComputerMaintenance](a, "GET", "/computers/maintenance/{id}", "computerMaintenance", "Computers", func(r *http.Request, _ httpcontract.Empty) (domain.ComputerMaintenance, error) {
		var computer domain.Computer
		if err := a.Config.Harness.Store.Get(r.Context(), "computers", r.PathValue("id"), &computer); err != nil {
			return domain.ComputerMaintenance{}, err
		}
		value, err := maintenanceCommands(computer)
		if err != nil {
			return value, err
		}
		status := a.Config.Harness.Hub.Status(computer)
		if comparison, err := releasefeed.Compare(status.DaemonVersion, a.Config.Version); err == nil {
			value.ServerNewer = comparison < 0
			value.DaemonNewer = comparison > 0
		}
		value.CanUpdate = !computer.Local && computer.Kind != "local" && !computer.Revoked && status.Online && a.Config.Harness.Hub.CanUpdateDaemon(computer.ID)
		value.Update = a.Config.Harness.Hub.DaemonUpdateStatus(computer.ID)
		return value, nil
	})
	route[httpcontract.Empty, domain.DaemonUpdate](a, "POST", "/computers/maintenance/{id}/update", "updateComputerDaemon", "Computers", func(r *http.Request, _ httpcontract.Empty) (domain.DaemonUpdate, error) {
		if r.ContentLength != 0 {
			var input struct{}
			if err := httpcontract.Decode(r, &input); err != nil {
				return domain.DaemonUpdate{}, err
			}
		}
		return a.Config.Harness.Hub.UpdateDaemon(r.Context(), r.PathValue("id"))
	})
}

func maintenanceCommands(computer domain.Computer) (domain.ComputerMaintenance, error) {
	value := domain.ComputerMaintenance{Manager: "unknown"}
	if computer.Local || computer.Kind == "local" {
		value.Manager = "desktop"
		return value, nil
	}
	if computer.Revoked {
		return value, domain.Forbidden("This Computer's connection has been revoked.")
	}
	quote := func(text string) string { return "'" + strings.ReplaceAll(text, "'", "'\\''") + "'" }
	psQuote := func(text string) string { return "'" + strings.ReplaceAll(text, "'", "''") + "'" }
	value.Command = "--update"
	value.PowerShellCommand = "-Update"
	if computer.Kind == "container" {
		if computer.Container == nil || computer.Container.Name == "" {
			return domain.ComputerMaintenance{}, domain.Conflict("Reconnect this container's Daemon so it can report its container name before updating.")
		}
		value.Manager = "docker"
		value.Command += " --container " + quote(computer.Container.Name)
		value.PowerShellCommand += " -ContainerID " + psQuote(computer.Container.Name)
	} else {
		if computer.Root == "" {
			return domain.ComputerMaintenance{}, domain.Conflict("Reconnect this Daemon so it can report its data directory before updating.")
		}
		value.Command += " --root " + quote(computer.Root)
		value.PowerShellCommand += " -Root " + psQuote(computer.Root)
		switch computer.OS {
		case "darwin":
			value.Manager = "launchd"
		case "linux":
			value.Manager = "systemd-user"
		case "windows":
			value.Manager = "task-scheduler"
		}
	}
	value.Command = computers.ShellInstallerCommand(value.Command)
	value.PowerShellCommand = computers.PowerShellInstallerCommand(value.PowerShellCommand)
	return value, nil
}

package daemonservice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StartUpdate launches a separate, visible current-user job. A child of the
// Daemon would be killed by systemd's cgroup or Windows' service lifecycle when
// the old Daemon stops, so ordinary process detachment is insufficient.
func (m *Manager) StartUpdate(ctx context.Context, executable string) error {
	if !filepath.IsAbs(executable) || strings.ContainsAny(executable, "\x00\r\n") {
		return fmt.Errorf("update executable must be an absolute path")
	}
	if info, err := os.Stat(executable); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("update executable is unavailable")
	}
	args := []string{"computer", "update", "--root", m.root, "--managed-helper"}
	log := filepath.Join(m.root, "update.log")
	switch m.platform {
	case "linux":
		_, err := m.command(ctx, "systemd-run", append([]string{"--user", "--collect", "--unit=astral-daemon-update-" + m.id, "--description=Astral Daemon update (" + m.id + ")", "--property=Type=exec", "--property=RuntimeMaxSec=180", "--", executable}, args...)...)
		return err
	case "darwin":
		label := "app.astral.daemon.update." + m.id
		state, e := m.run(ctx, "/bin/launchctl", "print", "gui/"+m.uid+"/"+label)
		if e != nil {
			return e
		}
		if state.code == 0 && strings.Contains(state.output, "state = running") {
			return fmt.Errorf("a Daemon update job is already running")
		}
		_, _ = m.run(ctx, "/bin/launchctl", "remove", label)
		_, err := m.command(ctx, "/bin/launchctl", append([]string{"submit", "-l", label, "-o", log, "-e", log, "--", executable}, args...)...)
		return err
	case "windows":
		target := "\\Astral\\Astral Daemon update (" + m.id + ")"
		query := windowsUpdateTaskQuery("Astral Daemon update (" + m.id + ")")
		state, e := m.command(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", query)
		if e != nil {
			return e
		}
		if strings.EqualFold(strings.TrimSpace(state), "running") {
			return fmt.Errorf("a Daemon update job is already running")
		}
		// EndBoundary plus DeleteExpiredTaskAfter removes a failed helper too.
		start := time.Now()
		end := start.Add(10 * time.Minute).Format("2006-01-02T15:04:05")
		argv := []string{}
		for _, arg := range args {
			argv = append(argv, windowsArg(arg))
		}
		xml := `<?xml version="1.0" encoding="UTF-16"?><Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><RegistrationInfo><Description>Astral Daemon update</Description></RegistrationInfo><Triggers><TimeTrigger><Enabled>false</Enabled><StartBoundary>` + start.Format("2006-01-02T15:04:05") + `</StartBoundary><EndBoundary>` + end + `</EndBoundary></TimeTrigger></Triggers><Principals><Principal id="Author"><UserId>` + m.uid + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals><Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><ExecutionTimeLimit>PT3M</ExecutionTimeLimit><DeleteExpiredTaskAfter>PT1M</DeleteExpiredTaskAfter><Hidden>false</Hidden></Settings><Actions Context="Author"><Exec><Command>` + xmlQuote(executable) + `</Command><Arguments>` + xmlQuote(strings.Join(argv, " ")) + `</Arguments></Exec></Actions></Task>`
		path := filepath.Join(m.root, "service", "update-task.xml")
		if err := writeAtomic(path, utf16XML(xml), 0600); err != nil {
			return err
		}
		defer os.Remove(path)
		if _, err := m.command(ctx, "schtasks.exe", "/create", "/xml", path, "/tn", target, "/f"); err != nil {
			return err
		}
		_, err := m.command(ctx, "schtasks.exe", "/run", "/tn", target)
		if err != nil {
			_, _ = m.run(context.WithoutCancel(ctx), "schtasks.exe", "/delete", "/tn", target, "/f")
		}
		return err
	}
	return fmt.Errorf("update jobs unsupported on %s", m.platform)
}

func windowsUpdateTaskQuery(name string) string {
	// A missing first-run task is expected. Without an explicit success exit,
	// Windows PowerShell can return the failed cmdlet's status even though its
	// missing-task error was suppressed and the conditional completed normally.
	return `$ErrorActionPreference='Stop'; $task=Get-ScheduledTask -TaskPath '\Astral\' -TaskName ` + psQuote(name) + ` -ErrorAction SilentlyContinue; if($null -ne $task){$task.State.ToString()}; exit 0`
}

// FinishUpdate unregisters the one-shot job without touching the Daemon service.
func (m *Manager) FinishUpdate(ctx context.Context) {
	switch m.platform {
	case "darwin":
		_, _ = m.run(ctx, "/bin/launchctl", "remove", "app.astral.daemon.update."+m.id)
	case "windows":
		_, _ = m.run(ctx, "schtasks.exe", "/delete", "/tn", "\\Astral\\Astral Daemon update ("+m.id+")", "/f")
	}
}

package daemonservice

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Prepare checks supervision before enrollment consumes its one-time token.
// Root is still a user-service account: only this explicit background install
// prepares its existing systemd user manager and logout/boot lifetime.
// Read-only status and normal service operations never enable lingering.
func (m *Manager) Prepare(ctx context.Context, report func(string)) error {
	if m.platform != "linux" {
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if m.uid != "0" {
		if err := m.probeUserManager(bounded); err != nil {
			return fmt.Errorf("a running systemd user manager for UID %s is required; use this account's Linux login session or ask its administrator to prepare the user manager (Astral will not elevate): %w", m.uid, err)
		}
		return nil
	}
	if report != nil {
		report("Preparing root's systemd user service: enabling root user lingering for logout/reboot and starting user@0.service. Astral Daemon remains a root-account user service.")
	}
	if _, err := m.command(bounded, "loginctl", "--no-ask-password", "enable-linger", "0"); err != nil {
		return fmt.Errorf("prepare root user lingering (Linux must be booted with systemd and provide loginctl): %w", err)
	}
	if _, err := m.command(bounded, "systemctl", "--no-ask-password", "start", "user@0.service"); err != nil {
		return fmt.Errorf("start root's systemd user manager: %w", err)
	}
	for {
		if err := m.probeUserManager(bounded); err == nil {
			return nil
		} else if bounded.Err() != nil {
			return fmt.Errorf("root's systemd user manager did not become available: %w", err)
		}
		if err := wait(bounded, 100*time.Millisecond); err != nil {
			return fmt.Errorf("root's systemd user manager did not become available: %w", err)
		}
	}
}

func (m *Manager) probeUserManager(ctx context.Context) error {
	// Never query show-environment: a manager may contain application secrets.
	result, err := m.run(ctx, "systemctl", "--user", "--no-pager", "show", "--property=Version", "--value")
	if err != nil {
		return err
	}
	if result.code != 0 {
		return fmt.Errorf("systemd user manager probe exited %d", result.code)
	}
	if strings.TrimSpace(result.output) == "" {
		return fmt.Errorf("systemd user manager did not report its version")
	}
	return nil
}

func userManagerEnvironment(environment []string, directory string, bus bool) []string {
	result := make([]string, 0, len(environment)+2)
	for _, variable := range environment {
		name, _, _ := strings.Cut(variable, "=")
		switch name {
		case "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "DBUS_SESSION_BUS_PID", "DBUS_SESSION_BUS_WINDOWID", "DBUS_STARTER_ADDRESS", "DBUS_STARTER_BUS_TYPE", "SYSTEMCTL_FORCE_BUS":
			continue
		}
		result = append(result, variable)
	}
	result = append(result, "XDG_RUNTIME_DIR="+directory)
	if bus {
		result = append(result, "DBUS_SESSION_BUS_ADDRESS=unix:path="+directory+"/bus")
	}
	return result
}

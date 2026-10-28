package daemonservice

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRootPreparationUsesExistingUserManagerAndReportsAccountLifetime(t *testing.T) {
	var calls, reports []string
	manager, err := newManager(t.TempDir(), t.TempDir(), "linux", "0", func(_ context.Context, name string, args ...string) (commandResult, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return commandResult{output: "255"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := manager.Prepare(context.Background(), func(message string) { reports = append(reports, message) }); err != nil {
			t.Fatal(err)
		}
	}
	expected := []string{
		"loginctl --no-ask-password enable-linger 0",
		"systemctl --no-ask-password start user@0.service",
		"systemctl --user --no-pager show --property=Version --value",
	}
	if !slices.Equal(calls[:3], expected) || !slices.Equal(calls[3:], expected) {
		t.Fatalf("root preparation must be repeatable and remain a user service: %v", calls)
	}
	if len(reports) != 2 || !strings.Contains(reports[0], "logout/reboot") || !strings.Contains(reports[0], "root-account user service") {
		t.Fatalf("account-level lifetime change was not visible: %v", reports)
	}
	if filepath.Base(filepath.Dir(manager.definition)) != "user" {
		t.Fatalf("root daemon was assigned a system service: %s", manager.definition)
	}
}

func TestPreparationStopsOnRootBootstrapFailure(t *testing.T) {
	for _, failAt := range []string{"loginctl", "systemctl"} {
		t.Run(failAt, func(t *testing.T) {
			var calls []string
			manager, _ := newManager(t.TempDir(), t.TempDir(), "linux", "0", func(_ context.Context, name string, _ ...string) (commandResult, error) {
				calls = append(calls, name)
				if name == failAt {
					return commandResult{code: 1, output: "manager unavailable"}, nil
				}
				return commandResult{}, nil
			})
			if err := manager.Prepare(context.Background(), nil); err == nil {
				t.Fatal("failed bootstrap was accepted")
			}
			if (failAt == "loginctl" && len(calls) != 1) || (failAt == "systemctl" && len(calls) != 2) {
				t.Fatalf("preparation continued after failure: %v", calls)
			}
		})
	}
}

func TestUserPreparationDoesNotElevateOrRequireAnActiveDefaultTarget(t *testing.T) {
	var calls []string
	manager, _ := newManager(t.TempDir(), t.TempDir(), "linux", "1001", func(_ context.Context, name string, args ...string) (commandResult, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return commandResult{output: "252.39"}, nil
	})
	if err := manager.Prepare(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"systemctl --user --no-pager show --property=Version --value"}) {
		t.Fatalf("ordinary user preparation changed account/system state: %v", calls)
	}
	manager.run = func(context.Context, string, ...string) (commandResult, error) {
		return commandResult{code: 1, output: "UNRELATED_MANAGER_SECRET"}, nil
	}
	err := manager.Prepare(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "UID 1001") || !strings.Contains(err.Error(), "will not elevate") || strings.Contains(err.Error(), "UNRELATED_MANAGER_SECRET") {
		t.Fatalf("manager failure must explain account scope without printing environment: %v", err)
	}
}

func TestRootManagerWaitIsBoundedAndProbeOutputIsNotDisclosed(t *testing.T) {
	manager, _ := newManager(t.TempDir(), t.TempDir(), "linux", "0", func(_ context.Context, _ string, args ...string) (commandResult, error) {
		if slices.Contains(args, "--user") {
			return commandResult{code: 1, output: "UNRELATED_MANAGER_SECRET"}, nil
		}
		return commandResult{}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	err := manager.Prepare(ctx, nil)
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "UNRELATED_MANAGER_SECRET") {
		t.Fatalf("expected bounded safe failure: %v", err)
	}
}

func TestOtherPlatformsDoNotPrepareLinuxServices(t *testing.T) {
	for _, platform := range []string{"darwin", "windows"} {
		uid := "0"
		if platform == "windows" {
			uid = "S-1-5-21-1"
		}
		manager, _ := newManager(t.TempDir(), t.TempDir(), platform, uid, func(context.Context, string, ...string) (commandResult, error) {
			t.Fatal("non-Linux preparation invoked a command")
			return commandResult{}, nil
		})
		if err := manager.Prepare(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUserManagerEnvironmentReplacesInheritedSessionWithoutChangingOtherVariables(t *testing.T) {
	environment := []string{"PATH=/bin", "HOME=/root", "XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "DBUS_SESSION_BUS_PID=123", "DBUS_STARTER_ADDRESS=other", "SYSTEMCTL_FORCE_BUS=1", "APP_SECRET=preserve-without-printing"}
	for _, bus := range []bool{false, true} {
		actual := userManagerEnvironment(environment, "/run/user/0", bus)
		expected := []string{"PATH=/bin", "HOME=/root", "APP_SECRET=preserve-without-printing", "XDG_RUNTIME_DIR=/run/user/0"}
		if bus {
			expected = append(expected, "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/0/bus")
		}
		if !slices.Equal(actual, expected) {
			t.Fatalf("unexpected normalized variable names/values; bus=%t", bus)
		}
	}
}

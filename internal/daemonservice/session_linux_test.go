package daemonservice

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestLinuxRuntimeDirectoryRejectsUnsafeOwnershipPermissionsAndLinks(t *testing.T) {
	for _, kind := range []string{"missing", "public", "wrong-owner", "symlink", "file"} {
		t.Run(kind, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "runtime")
			uid := os.Geteuid()
			if kind != "missing" && kind != "symlink" && kind != "file" {
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "public":
				if err := os.Chmod(directory, 0755); err != nil {
					t.Fatal(err)
				}
			case "wrong-owner":
				uid++
			case "symlink":
				if err := os.Symlink(t.TempDir(), directory); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(directory, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := linuxUserEnvironment(directory, uid, nil); err == nil {
				t.Fatal("unsafe runtime directory was accepted")
			}
		})
	}
}

func TestLinuxRuntimeSupportsPrivateOnlyAndRealOwnedSessionBus(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	inherited := []string{"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/another/bus", "XDG_RUNTIME_DIR=/run/user/another"}
	environment, err := linuxUserEnvironment(directory, os.Geteuid(), inherited)
	if err != nil || !slices.Equal(environment, []string{"XDG_RUNTIME_DIR=" + directory}) {
		t.Fatalf("private-only manager must not require an installed session bus: %v", err)
	}
	listener, err := net.Listen("unix", filepath.Join(directory, "bus"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	environment, err = linuxUserEnvironment(directory, os.Geteuid(), inherited)
	if err != nil || !slices.Contains(environment, "DBUS_SESSION_BUS_ADDRESS=unix:path="+directory+"/bus") {
		t.Fatalf("owned session bus was not selected: %v", err)
	}
}

func TestLinuxRuntimeRejectsFakeOrLinkedSessionBus(t *testing.T) {
	for _, linked := range []bool{false, true} {
		directory := t.TempDir()
		if err := os.Chmod(directory, 0700); err != nil {
			t.Fatal(err)
		}
		bus := filepath.Join(directory, "bus")
		var err error
		if linked {
			err = os.Symlink("/nonexistent", bus)
		} else {
			err = os.WriteFile(bus, nil, 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := linuxUserEnvironment(directory, os.Geteuid(), nil); err == nil {
			t.Fatal("non-socket user bus was accepted")
		}
	}
}

func TestLinuxRunnerCannotTargetAnotherEffectiveAccount(t *testing.T) {
	_, err := nativeRunner(strconv.Itoa(os.Geteuid()+1))(context.Background(), "systemctl", "--user", "show")
	if err == nil || !strings.Contains(err.Error(), "effective Linux user") {
		t.Fatalf("runner accepted another effective account: %v", err)
	}
}

package daemonservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

func nativeRunner(uid string) commandRunner {
	return func(ctx context.Context, name string, args ...string) (commandResult, error) {
		if uid != strconv.Itoa(os.Geteuid()) {
			return commandResult{}, errors.New("service account does not match the effective Linux user")
		}
		environment := os.Environ()
		if (name == "systemctl" || name == "systemd-run") && slices.Contains(args, "--user") {
			var err error
			environment, err = linuxUserEnvironment(filepath.Join("/run/user", uid), os.Geteuid(), environment)
			if err != nil {
				return commandResult{}, err
			}
		} else if uid == "0" && (name == "systemctl" || name == "loginctl") {
			// Preparation must contact this machine's system manager, not a bus
			// address inherited through sudo/su from another account.
			environment = slices.DeleteFunc(environment, func(variable string) bool {
				return strings.HasPrefix(variable, "DBUS_SYSTEM_BUS_ADDRESS=")
			})
		}
		return executeEnvironment(ctx, environment, name, args...)
	}
}

func linuxUserEnvironment(directory string, uid int, environment []string) ([]string, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("systemd user runtime directory %s is unavailable: %w", directory, err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(uid) || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("systemd user runtime directory %s must be a private directory owned by effective UID %d", directory, uid)
	}
	bus, err := os.Lstat(filepath.Join(directory, "bus"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect systemd user bus: %w", err)
	}
	if err == nil {
		owner, ok := bus.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(uid) || bus.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("systemd user bus must be a socket owned by effective UID %d", uid)
		}
	}
	// A session bus is optional here. Current systemctl and systemd-run can
	// connect directly to systemd/private; the actual manager probe decides.
	return userManagerEnvironment(environment, directory, err == nil), nil
}

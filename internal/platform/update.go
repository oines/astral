package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/daemonservice"
	"github.com/oines/astral/internal/releasefeed"
)

// DaemonVersion is set by the executable entry point before any Daemon starts.
var DaemonVersion = "dev"

type UpdateOptions struct{ Root, Container, Version string }

// UpdateComputer is deliberately a local command. The caller downloads this
// release first, then executes it on the computer being updated.
func UpdateComputer(ctx context.Context, options UpdateOptions, report func(string)) error {
	if report == nil {
		report = func(string) {}
	}
	if options.Container != "" {
		if options.Root != "" {
			return fmt.Errorf("choose --root or --container")
		}
		identity := strings.TrimPrefix(options.Container, "astral-")
		if !identityPattern.MatchString(identity) {
			return fmt.Errorf("invalid container identity")
		}
		probe, err := ProbeDocker(ctx)
		if err != nil {
			return err
		}
		directory, err := os.MkdirTemp("", "astral-update-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(directory)
		binary := filepath.Join(directory, "astral")
		release, err := releasefeed.Latest(ctx, "linux", probe.Arch)
		if err != nil {
			return err
		}
		if err = release.Download(ctx, binary); err != nil {
			return err
		}
		report("Updating the Daemon in the existing container; restarting it interrupts running tasks. Its files and installed software are retained.")
		return UpdateContainerDaemon(dockerContext(ctx, probe), identity, binary, release.Version, report)
	}
	if options.Root == "" {
		return fmt.Errorf("--root or --container is required; copy the update command from Computer details")
	}
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return err
	}
	lock, err := Lock(filepath.Join(root, "update.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	var credentials computers.Credentials
	if err = ReadJSON(filepath.Join(root, "credentials.json"), &credentials); err != nil {
		return err
	}
	if credentials.ComputerID == "" {
		return fmt.Errorf("this directory has no enrolled Computer")
	}
	manager, err := daemonservice.New(root)
	if err != nil {
		return err
	}
	var previous daemonservice.Spec
	if err = ReadJSON(filepath.Join(root, "service", "spec.json"), &previous); err != nil {
		return fmt.Errorf("no managed Daemon service; install its current-user service before updating: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	before, _ := computerStatus(ctx, credentials)
	report("Updating Astral Daemon to " + options.Version + "; restarting its current-user service interrupts running tasks.")
	next := previous
	next.Executable = executable
	if err = manager.Install(ctx, next); err == nil {
		err = manager.Restart(ctx)
	}
	if err == nil {
		wait, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = waitUpdatedComputer(wait, credentials, before.BootID, options.Version)
		cancel()
	}
	if err == nil {
		pruneManagedDaemonReleases(root, executable, previous.Executable)
		report("Astral Daemon updated and online: " + credentials.ComputerID)
		return nil
	}
	// A failed release must not strand the user with only a broken definition.
	recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	restore := manager.Reinstall(recovery, previous)
	if restore == nil {
		restore = manager.Restart(recovery)
	}
	if restore == nil {
		restore = waitUpdatedComputer(recovery, credentials, before.BootID, before.DaemonVersion)
	}
	if restore != nil {
		return errors.Join(fmt.Errorf("daemon update failed: %w", err), fmt.Errorf("restore previous service failed; credentials and Agent files remain at %s: %w", root, restore))
	}
	return fmt.Errorf("daemon update failed; the previous service executable was restored: %w", err)
}

// Keep the current and rollback programs, rather than accumulating every
// release in a frequently updated Daemon's private staging directory.
func pruneManagedDaemonReleases(root string, keep ...string) {
	base := filepath.Join(root, "service", "releases")
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, entry := range entries {
		path := filepath.Join(base, entry.Name())
		retained := false
		for _, program := range keep {
			if filepath.Dir(program) == path || runtime.GOOS == "windows" && strings.EqualFold(filepath.Dir(program), path) {
				retained = true
			}
		}
		if !retained {
			_ = os.RemoveAll(path)
		}
	}
}

func waitUpdatedComputer(ctx context.Context, credentials computers.Credentials, previousBoot, version string) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		status, err := computerStatus(ctx, credentials)
		if err == nil && status.Online && status.BootID != previousBoot && (version == "" || status.DaemonVersion == version) {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("new Daemon did not reconnect with version %s: %w", version, errors.Join(ctx.Err(), last))
		case <-ticker.C:
		}
	}
}

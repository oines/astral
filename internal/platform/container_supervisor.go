package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/releasefeed"
)

type containerSupervisorPaths struct {
	binary, backup, request string
	reconnectTimeout        time.Duration
	verify                  func(context.Context, releasefeed.Binary) error
}

type supervisedDaemon struct {
	command *exec.Cmd
	done    chan error
}

func startSupervisedDaemon(binary, root, enrollment string, out, stderr io.Writer) (*supervisedDaemon, error) {
	args := []string{"computer", "daemon", "--root", root, "--credentials", filepath.Join(root, "credentials.json")}
	if _, err := os.Stat(filepath.Join(root, "credentials.json")); errors.Is(err, os.ErrNotExist) {
		args = []string{"computer", "connect", "--root", root, "--enrollment-file", enrollment, "--foreground"}
	}
	command := exec.Command(binary, args...)
	command.Env = append(os.Environ(), "ASTRAL_CONTAINER_SUPERVISOR=1")
	command.Stdout, command.Stderr = out, stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	child := &supervisedDaemon{command: command, done: make(chan error, 1)}
	go func() { child.done <- command.Wait() }()
	return child, nil
}

func stopSupervisedDaemon(child *supervisedDaemon) error {
	if child == nil {
		return nil
	}
	_ = child.command.Process.Signal(os.Interrupt)
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-child.done:
		return nil
	case <-timer.C:
	}
	_ = child.command.Process.Kill()
	select {
	case <-child.done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("container Daemon did not stop")
	}
}

// SuperviseContainer keeps the existing container alive while replacing only
// its executable. The same Astral binary provides both supervisor and Daemon;
// no Docker socket, host helper, volume replacement or image rebuild is used.
func SuperviseContainer(ctx context.Context, root, enrollment string, out, stderr io.Writer) error {
	if runtime.GOOS != "linux" || os.Getenv("ASTRAL_CONTAINER_INFO") == "" {
		return errors.New("computer supervise is only available inside an Astral Linux container")
	}
	return superviseContainer(ctx, root, enrollment, out, stderr, containerSupervisorPaths{binary: "/usr/local/bin/astral", backup: "/usr/local/bin/.astral-previous", request: containerUpdateRequest, reconnectTimeout: 30 * time.Second})
}

func superviseContainer(ctx context.Context, root, enrollment string, out, stderr io.Writer, paths containerSupervisorPaths) error {
	lock, err := Lock(filepath.Join(root, "container-supervisor.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	binary, backup := paths.binary, paths.backup
	// An interrupted transaction is recovered before accepting new work.
	if _, e := os.Stat(backup); e == nil {
		if err = os.Rename(backup, binary); err != nil {
			return fmt.Errorf("restore interrupted container update: %w", err)
		}
		_ = os.Remove(paths.request)
	}
	child, err := startSupervisedDaemon(binary, root, enrollment, out, stderr)
	if err != nil {
		return err
	}
	defer func() { _ = stopSupervisedDaemon(child) }()
	rejectUpdate := func(reason error) error {
		// The old Daemon has already handed off this request and keeps its update
		// guard until exit. Restart the unchanged program on rejection as well, so
		// the next explicit update can run without changing the handoff protocol.
		// Remove the request first: the new child must not inherit rejected work.
		if err := os.Remove(paths.request); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove rejected container update: %w", err)
		}
		fmt.Fprintln(stderr, "Container update rejected; restarting current Daemon:", reason)
		if err := stopSupervisedDaemon(child); err != nil {
			return err
		}
		child = nil
		if ctx.Err() != nil {
			return nil
		}
		child, err = startSupervisedDaemon(binary, root, enrollment, out, stderr)
		return err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case err = <-child.done:
			child = nil
			if err == nil {
				return errors.New("container Daemon exited unexpectedly")
			}
			return fmt.Errorf("container Daemon exited: %w", err)
		case <-ticker.C:
		}
		var update containerUpdate
		if err = ReadJSON(paths.request, &update); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read staged container update: %w", err)
		}
		if err = validateContainerStage(update, filepath.Dir(binary)); err != nil {
			if err = rejectUpdate(err); err != nil {
				return err
			}
			continue
		}
		var credentials computers.Credentials
		if err = ReadJSON(filepath.Join(root, "credentials.json"), &credentials); err != nil {
			return err
		}
		before, e := computerStatus(ctx, credentials)
		if e != nil || before.BootID != update.PreviousBoot {
			if err = rejectUpdate(errors.New("original Daemon is no longer connected")); err != nil {
				return err
			}
			continue
		}
		if comparison, e := releasefeed.Compare(update.Binary.Version, before.DaemonVersion); e == nil && comparison <= 0 {
			if err = rejectUpdate(errors.New("target is not newer than the current Daemon")); err != nil {
				return err
			}
			continue
		}
		verify := paths.verify
		if verify == nil {
			verify = func(ctx context.Context, b releasefeed.Binary) error { return b.Verify(ctx) }
		}
		if err = verify(ctx, update.Binary); err != nil {
			if err = rejectUpdate(err); err != nil {
				return err
			}
			continue
		}
		if err = stopSupervisedDaemon(child); err != nil {
			return err
		}
		child = nil
		// Recheck after the old Daemon has reaped command processes: a staged
		// path may have changed while the official manifest was being fetched.
		if err = validateContainerStage(update, filepath.Dir(binary)); err != nil {
			fmt.Fprintln(stderr, "Container update rejected:", err)
			_ = os.Remove(paths.request)
			child, err = startSupervisedDaemon(binary, root, enrollment, out, stderr)
			if err != nil {
				return err
			}
			continue
		}
		if err = replaceContainerProgram(binary, backup, update); err != nil {
			fmt.Fprintln(stderr, "Container update failed:", err)
			_ = os.Remove(paths.request)
			child, err = startSupervisedDaemon(binary, root, enrollment, out, stderr)
			if err != nil {
				return err
			}
			continue
		}
		child, err = startSupervisedDaemon(binary, root, enrollment, out, stderr)
		if err == nil {
			wait, cancel := context.WithTimeout(ctx, paths.reconnectTimeout)
			err = waitUpdatedComputer(wait, credentials, before.BootID, update.Binary.Version)
			cancel()
		}
		if err == nil {
			_ = os.Remove(backup)
			_ = os.Remove(paths.request)
			fmt.Fprintln(out, "Astral Daemon updated in the original container to", update.Binary.Version)
			continue
		}
		fmt.Fprintln(stderr, "Container update failed; restoring previous program:", err)
		if e = stopSupervisedDaemon(child); e != nil {
			return e
		}
		child = nil
		if e = os.Rename(backup, binary); e != nil {
			return fmt.Errorf("container update recovery failed; previous program retained at %s: %w", backup, e)
		}
		_ = os.Remove(paths.request)
		if ctx.Err() != nil {
			return nil
		}
		child, err = startSupervisedDaemon(binary, root, enrollment, out, stderr)
		if err != nil {
			return err
		}
	}
}

func validateContainerStage(update containerUpdate, directory string) error {
	if err := update.Binary.Validate("linux", runtime.GOARCH); err != nil {
		return err
	}
	if update.Path != filepath.Join(directory, ".astral-release-"+update.Binary.SHA256) || update.PreviousBoot == "" {
		return errors.New("invalid staged container update path or boot identity")
	}
	info, err := os.Lstat(update.Path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("staged update must be a regular executable")
	}
	file, err := os.Open(update.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, io.LimitReader(file, (256<<20)+1)); err != nil {
		return err
	}
	if info.Size() > 256<<20 || hex.EncodeToString(hash.Sum(nil)) != update.Binary.SHA256 {
		return errors.New("staged container executable checksum mismatch")
	}
	return nil
}

func replaceContainerProgram(binary, backup string, update containerUpdate) error {
	if err := os.Rename(binary, backup); err != nil {
		return err
	}
	if err := os.Rename(update.Path, binary); err != nil {
		return errors.Join(err, os.Rename(backup, binary))
	}
	return nil
}

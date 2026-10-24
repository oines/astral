package platform

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
)

// UpdateContainerDaemon replaces only the enrolled program inside the existing
// container. It never creates/removes a container or touches its data volume,
// installed packages, enrollment, or writable workspace.
func UpdateContainerDaemon(ctx context.Context, identity, binaryPath, expectedVersion string, report func(string)) (updateErr error) {
	if !identityPattern.MatchString(identity) || strings.TrimSpace(expectedVersion) == "" {
		return domain.Invalid("container identity and expected version are required")
	}
	if report == nil {
		report = func(string) {}
	}
	probe, err := ProbeDocker(ctx)
	if err != nil {
		return err
	}
	ctx = dockerContext(ctx, probe)
	name := "astral-" + identity
	raw, err := dockerOutput(ctx, "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return err
	}
	var container struct {
		ID     string
		Config struct{ Labels map[string]string }
		State  struct{ Running bool }
	}
	if json.Unmarshal(raw, &container) != nil || container.ID == "" || container.Config.Labels["astral.operation"] != identity {
		return domain.Conflict("the container does not belong to this Astral operation")
	}
	if !container.State.Running {
		return domain.Offline("start the existing container before updating its Daemon")
	}
	id := container.ID
	raw, err = dockerOutput(ctx, "exec", id, "cat", "/var/lib/astral/credentials.json")
	if err != nil {
		return fmt.Errorf("read existing container enrollment: %w", err)
	}
	var credentials computers.Credentials
	if json.Unmarshal(raw, &credentials) != nil || credentials.ComputerID == "" {
		return errors.New("existing container has no usable enrolled Computer credentials")
	}
	// host.docker.internal is a container DNS name. The bridge certificate also
	// covers loopback so the local updater can verify the same pinned listener.
	if parsed, parseErr := url.Parse(credentials.ServerURL); parseErr == nil && credentials.ServerCA != "" && parsed.Hostname() == "host.docker.internal" {
		parsed.Host = net.JoinHostPort("127.0.0.1", parsed.Port())
		credentials.ServerURL = parsed.String()
	}
	before, _ := computerStatus(ctx, credentials)
	file, err := os.Open(binaryPath)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	nonce := make([]byte, 8)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	suffix := hex.EncodeToString(nonce)
	stage := "/usr/local/bin/.astral-update-" + suffix
	backup := "/usr/local/bin/.astral-backup-" + suffix
	const lock = "/usr/local/bin/.astral-update-lock"
	if _, err = dockerOutput(ctx, "exec", id, "mkdir", lock); err != nil {
		return fmt.Errorf("another update may be active in this container; inspect %s: %w", lock, err)
	}
	keepBackup := false
	defer func() {
		cleanup, cancel := context.WithTimeout(dockerContext(context.Background(), probe), 10*time.Second)
		defer cancel()
		paths := []string{"exec", id, "rm", "-f", stage}
		if !keepBackup {
			paths = append(paths, backup)
		}
		_, _ = dockerOutput(cleanup, paths...)
		_, _ = dockerOutput(cleanup, "exec", id, "rmdir", lock)
	}()
	directory, err := os.MkdirTemp("", "astral-container-backup-")
	if err != nil {
		return err
	}
	defer func() {
		if !keepBackup {
			_ = os.RemoveAll(directory)
		}
	}()
	previous := filepath.Join(directory, "astral")
	if _, err = dockerOutput(ctx, "cp", id+":/usr/local/bin/astral", previous); err != nil {
		return fmt.Errorf("back up current Daemon: %w", err)
	}
	report("Copying verified Daemon into the existing container " + name + ".")
	if _, err = dockerOutput(ctx, "cp", binaryPath, id+":"+stage); err != nil {
		return err
	}
	check := `set -eu; actual=$(sha256sum "$1"); test "${actual%% *}" = "$2"; chmod 755 "$1"`
	if _, err = dockerOutput(ctx, "exec", id, "/bin/sh", "-c", check, "astral-verify", stage, digest); err != nil {
		return fmt.Errorf("verify staged container Daemon checksum: %w", err)
	}
	version, err := dockerOutput(ctx, "exec", id, stage, "version")
	if err != nil {
		return fmt.Errorf("run staged Daemon: %w", err)
	}
	if strings.TrimSpace(string(version)) != expectedVersion {
		return fmt.Errorf("staged Daemon version is %q, expected %q", strings.TrimSpace(string(version)), expectedVersion)
	}
	replaced := false
	defer func() {
		if updateErr == nil || !replaced {
			return
		}
		recovery, cancel := context.WithTimeout(dockerContext(context.Background(), probe), 45*time.Second)
		defer cancel()
		report("Update failed; restoring the previous Daemon in the same container.")
		_, _ = dockerOutput(recovery, "stop", "--time", "10", id)
		restoreErr := restoreContainerBinary(recovery, id, previous)
		if restoreErr == nil {
			_, restoreErr = dockerOutput(recovery, "start", id)
		}
		if restoreErr == nil {
			if before.DaemonVersion != "" {
				restoreErr = waitUpdatedComputer(recovery, credentials, "", before.DaemonVersion)
			} else {
				restoreErr = waitContainerOnline(recovery, credentials)
			}
		}
		if restoreErr != nil {
			keepBackup = true
			updateErr = errors.Join(updateErr, fmt.Errorf("previous Daemon restore failed; original container %s and volume remain intact; previous program retained at host %s and container %s: %w", id, previous, backup, restoreErr))
		} else {
			updateErr = fmt.Errorf("daemon update failed; previous program restored in the original container: %w", updateErr)
		}
	}()
	// A lost Docker response cannot prove that mv did not happen. Restore the
	// known old program on every uncertain mutation outcome as well.
	replaced = true
	if _, err = dockerOutput(ctx, "exec", id, "/bin/sh", "-c", `set -eu; cp -p /usr/local/bin/astral "$2"; mv -f "$1" /usr/local/bin/astral`, "astral-replace", stage, backup); err != nil {
		return err
	}
	report("Restarting the same container; running tasks will be interrupted, files and installed software remain.")
	if _, err = dockerOutput(ctx, "restart", "--time", "10", id); err != nil {
		return err
	}
	version, err = dockerOutput(ctx, "exec", id, "/usr/local/bin/astral", "version")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(version)) != expectedVersion {
		return errors.New("container restarted with an unexpected Daemon version")
	}
	wait, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err = waitUpdatedComputer(wait, credentials, before.BootID, expectedVersion); err != nil {
		return err
	}
	report("Astral Daemon updated and online in the original container: " + name)
	return nil
}

// Explicit tar metadata is essential on Windows: a binary exported to NTFS
// loses Unix executable bits. Restoring by ordinary host docker cp would make
// the stopped container's entrypoint non-executable.
func restoreContainerBinary(ctx context.Context, id, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	written := make(chan error, 1)
	go func() {
		archive := tar.NewWriter(writer)
		err := archive.WriteHeader(&tar.Header{Name: "astral", Mode: 0755, Size: info.Size(), Typeflag: tar.TypeReg, ModTime: time.Now()})
		if err == nil {
			_, err = io.Copy(archive, file)
		}
		err = errors.Join(err, archive.Close())
		_ = writer.CloseWithError(err)
		written <- err
	}()
	command := dockerCommand(ctx, "cp", "-", id+":/usr/local/bin")
	command.Stdin = reader
	raw, runErr := command.CombinedOutput()
	_ = reader.Close()
	writeErr := <-written
	if err = errors.Join(runErr, writeErr); err != nil {
		return fmt.Errorf("restore executable archive into original container: %w: %s", err, strings.TrimSpace(string(raw)))
	}
	return nil
}

func waitContainerOnline(ctx context.Context, credentials computers.Credentials) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if status, err := computerStatus(ctx, credentials); err == nil && status.Online {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

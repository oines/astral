package platform

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func ensureComputerImage(ctx context.Context, image, arch string, report func(string)) error {
	inspect := func() error {
		raw, err := dockerOutput(ctx, "image", "inspect", "--format", "{{json .}}", image)
		if err != nil {
			return err
		}
		var metadata struct{ OS, Architecture string }
		if json.Unmarshal(raw, &metadata) != nil || metadata.OS != "linux" || metadata.Architecture != arch {
			return fmt.Errorf("the Computer environment image does not match linux/%s", arch)
		}
		return nil
	}
	if err := inspect(); err == nil {
		if report != nil {
			report("Using the downloaded Computer environment.")
		}
		return nil
	}
	if report != nil {
		report("Downloading the Computer environment.")
	}
	if err := runDocker(ctx, report, "pull", "--platform", "linux/"+arch, image); err != nil {
		return err
	}
	return inspect()
}

func containerAwaitingFirstStart(ctx context.Context, id string) (bool, error) {
	raw, err := dockerOutput(ctx, "inspect", "--format", "{{.State.Status}}", id)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(raw)) == "created", nil
}

// Retrying a create interrupted before injection must complete the original
// container. Never replace the program in a container which already started.
func injectCreatedContainer(ctx context.Context, id, binary string, report func(string)) error {
	created, err := containerAwaitingFirstStart(ctx, id)
	if err != nil || !created {
		return err
	}
	if report != nil {
		report("Finishing the Daemon installation in the existing container.")
	}
	return injectContainerDaemon(ctx, id, binary)
}

// Explicit tar permissions also work when the source is on Windows, where a
// Linux executable copied as a plain host file may otherwise lose its x bits.
func injectContainerDaemon(ctx context.Context, id, binary string) error {
	file, err := os.Open(binary)
	if err != nil {
		return fmt.Errorf("open the Linux Daemon executable: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("the Linux Daemon executable must be a nonempty regular file")
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	written := make(chan error, 1)
	go func() {
		archive := tar.NewWriter(writer)
		writeErr := archive.WriteHeader(&tar.Header{Name: "astral", Mode: 0755, Size: info.Size(), Typeflag: tar.TypeReg})
		if writeErr == nil {
			_, writeErr = io.CopyN(archive, file, info.Size())
		}
		writeErr = errors.Join(writeErr, archive.Close())
		_ = writer.CloseWithError(writeErr)
		written <- writeErr
	}()
	command := dockerCommand(ctx, "cp", "-", id+":/usr/local/bin/")
	command.Stdin = reader
	raw, copyErr := command.CombinedOutput()
	_ = reader.CloseWithError(copyErr)
	writeErr := <-written
	if err = errors.Join(copyErr, writeErr); err != nil {
		return fmt.Errorf("install the Daemon into the container: %w: %s", err, strings.TrimSpace(string(raw)))
	}
	return nil
}

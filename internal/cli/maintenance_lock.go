package cli

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/platform"
)

// maintenanceWithDataLock delegates an offline operation without opening a
// database. The process locks are held until the exact child command exits.
func maintenanceWithDataLock(ctx context.Context, args []string, out, stderr io.Writer) error {
	f := flags("maintenance with-data-lock", stderr)
	data := f.String("data", "", "existing Server data directory")
	computers := f.String("computers-dir", "", "existing Computers data directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	command := f.Args()
	if strings.TrimSpace(*data) == "" || strings.TrimSpace(*computers) == "" || len(command) == 0 {
		return fmt.Errorf("usage: astral maintenance with-data-lock --data DIRECTORY --computers-dir DIRECTORY -- COMMAND [ARGUMENTS...]")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dataRoot, err := maintenanceDirectory(*data)
	if err != nil {
		return err
	}
	computerRoot, err := maintenanceDirectory(*computers)
	if err != nil {
		return err
	}
	var held []*os.File
	defer func() {
		for index := len(held) - 1; index >= 0; index-- {
			_ = held[index].Close()
		}
	}()
	acquire := func(file string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if info, err := os.Lstat(file); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("maintenance lock must be a regular file: %s", file)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		lock, err := platform.Lock(file)
		if err != nil {
			return fmt.Errorf("stop Astral Server/Desktop and Daemons before offline maintenance: %w", err)
		}
		held = append(held, lock)
		return nil
	}
	if err = acquire(filepath.Join(dataRoot, "server.lock")); err != nil {
		return err
	}
	err = filepath.WalkDir(computerRoot, func(file string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && file != computerRoot {
			switch entry.Name() {
			case "AgentHome", "agents", "node_modules", ".git":
				return filepath.SkipDir
			}
		}
		if entry.Name() == "daemon.lock" {
			if !entry.Type().IsRegular() {
				return fmt.Errorf("maintenance lock must be a regular file: %s", file)
			}
			return acquire(file)
		}
		return nil
	})
	if err != nil {
		return err
	}
	child := exec.CommandContext(ctx, command[0], command[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, out, stderr
	child.Env = append(os.Environ(),
		"ASTRAL_MAINTENANCE_LOCK_PID="+strconv.Itoa(os.Getpid()),
		"ASTRAL_MAINTENANCE_DATA="+dataRoot,
		"ASTRAL_MAINTENANCE_COMPUTERS="+computerRoot)
	// A cancelled child may have descendants retaining its output pipe. Wait for
	// its own exit, then bound pipe draining before releasing the process locks.
	child.WaitDelay = 5 * time.Second
	if err = child.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("maintenance command failed: %w", err)
	}
	return nil
}
func maintenanceDirectory(directory string) (string, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("existing maintenance directory required: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("maintenance path must be a directory: %s", canonical)
	}
	return canonical, nil
}

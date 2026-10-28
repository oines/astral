//go:build linux

package serverupdate

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func ownedFile(path string, uid int) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid
}
func processArguments(pid int) ([]string, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"), nil
}
func installCommand(ctx context.Context, name string, args []string, out, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = out, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	// TERM reaches both Bash and its foreground fetch, allowing the installer
	// trap to finish recovery. Default CommandContext SIGKILL bypasses that trap.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	grace := time.NewTimer(150 * time.Second)
	defer grace.Stop()
	select {
	case err := <-done:
		return errors.Join(ctx.Err(), err)
	case <-grace.C:
		// Reap the complete installer group before writing any terminal result.
		// Killing only Bash could leave a cleanup command mutating the service.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return errors.Join(ctx.Err(), <-done)
	}
}

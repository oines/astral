//go:build integration && !windows

package platform

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

func nativeInstallerCommand(ctx context.Context, script, server, pin, enrollment, token, root string, container bool) *exec.Cmd {
	args := []string{script, "--server", server, "--server-pin", pin, "--enrollment", enrollment, "--token", token, "--root", root}
	if container {
		args = append(args, "--container")
	} else {
		args = append(args, "--foreground")
	}
	command := exec.CommandContext(ctx, "/bin/sh", args...)
	// Cancel both the installer and its foreground Daemon, including its shell.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGTERM) }
	command.WaitDelay = 5 * time.Second
	return command
}

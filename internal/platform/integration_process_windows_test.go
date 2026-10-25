//go:build integration && windows

package platform

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

func nativeInstallerCommand(ctx context.Context, script, server, pin, enrollment, token, root string, container bool) *exec.Cmd {
	args := []string{"-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-Server", server, "-ServerPin", pin, "-Enrollment", enrollment, "-Token", token, "-Root", root}
	if container {
		args = append(args, "-Container")
	} else {
		args = append(args, "-Foreground")
	}
	// Match the copied installation command and validate Windows' built-in
	// PowerShell 5.1, regardless of the runner's preferred SHELL or installed pwsh.
	command := exec.CommandContext(ctx, "powershell.exe", args...)
	command.Cancel = func() error {
		// The installer owns a foreground Daemon child. Windows cannot send a
		// Unix group signal; taskkill's tree mode cleans the disposable test tree.
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return exec.CommandContext(cleanup, "taskkill.exe", "/F", "/T", "/PID", strconv.Itoa(command.Process.Pid)).Run()
	}
	command.WaitDelay = 5 * time.Second
	return command
}

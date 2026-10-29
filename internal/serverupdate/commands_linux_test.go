//go:build linux

package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInstallerCancellationRunsTERMRecoveryTrap(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "recovered")
	ready := filepath.Join(directory, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	sleep, err = filepath.EvalSymlinks(sleep)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	finished := make(chan struct{})
	var installerPID, childPID int
	go func() {
		err := installCommand(ctx, "/bin/bash", []string{"-c", `
trap 'wait "$child" 2>/dev/null; printf recovered > "$1"; exit 143' TERM
"$3" 30 &
child=$!
printf '%s %s\n' "$$" "$child" > "$2"
wait "$child"
`, "fixture", marker, ready, sleep}, io.Discard, io.Discard)
		close(finished)
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
			return
		default:
		}
		if installerPID > 0 {
			_ = syscall.Kill(-installerPID, syscall.SIGKILL)
		}
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("installer fixture did not exit during cleanup")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if raw, err := os.ReadFile(ready); err == nil {
			if n, err := fmt.Fscan(strings.NewReader(string(raw)), &installerPID, &childPID); err == nil && n == 2 {
				// Bash must finish forking and the child must finish exec before
				// TERM: an earlier ready signal can leave a new sleep holding the
				// os/exec output pipes after its parent has run the recovery trap.
				if executable, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", childPID)); err == nil && executable == sleep {
					break
				}
			}
		}
		select {
		case err := <-done:
			t.Fatalf("installer fixture exited before readiness: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("installer fixture did not initialize its rollback trap and child")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled installer did not report cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("installer rollback did not finish")
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "recovered" {
		t.Fatalf("installer was killed without its rollback trap: %q %v", raw, err)
	}
	for _, pid := range []int{installerPID, childPID} {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("installer fixture process %d was not reaped: %v", pid, err)
		}
	}
}

//go:build linux

package serverupdate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInstallerCancellationRunsTERMRecoveryTrap(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "recovered")
	ready := filepath.Join(directory, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- installCommand(ctx, "/bin/bash", []string{"-c", `trap 'printf recovered > "$1"; exit 143' TERM; printf ready > "$2"; sleep 30 & wait $!`, "fixture", marker, ready}, io.Discard, io.Discard)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("installer fixture did not initialize its rollback trap")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled installer reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("installer rollback did not finish")
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "recovered" {
		t.Fatalf("installer was killed without its rollback trap: %q %v", raw, err)
	}
}

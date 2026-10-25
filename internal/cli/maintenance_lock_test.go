package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/oines/astral/internal/platform"
)

func TestMaintenanceLockChild(t *testing.T) {
	if os.Getenv("ASTRAL_TEST_LOCK_CHILD") != "1" {
		return
	}
	arguments := os.Args
	separator := 0
	for index, value := range arguments {
		if value == "--" {
			separator = index
			break
		}
	}
	if separator == 0 || len(arguments) < separator+3 {
		os.Exit(80)
	}
	mode, marker := arguments[separator+1], arguments[separator+2]
	raw, _ := json.Marshal(map[string]string{
		"pid":       os.Getenv("ASTRAL_MAINTENANCE_LOCK_PID"),
		"data":      os.Getenv("ASTRAL_MAINTENANCE_DATA"),
		"computers": os.Getenv("ASTRAL_MAINTENANCE_COMPUTERS"),
		"ppid":      strconv.Itoa(os.Getppid()),
	})
	if os.WriteFile(marker+".pending", raw, 0600) != nil || os.Rename(marker+".pending", marker) != nil {
		os.Exit(81)
	}
	switch mode {
	case "wait":
		time.Sleep(30 * time.Second)
	case "fail":
		os.Exit(42)
	}
	os.Exit(0)
}
func maintenanceLockFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	data, computers := filepath.Join(root, "server"), filepath.Join(root, "computers")
	for _, directory := range []string{data, filepath.Join(computers, "local"), filepath.Join(computers, "agents", "home")} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	daemon := filepath.Join(computers, "local", "daemon.lock")
	if err := os.WriteFile(daemon, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return data, computers, daemon
}
func maintenanceChildArguments(t *testing.T, data, computers, mode, marker string) []string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{"--data", data, "--computers-dir", computers, "--", executable, "-test.run=^TestMaintenanceLockChild$", "--", mode, marker}
}
func TestMaintenanceWithDataLockHoldsActualProcessLocksUntilChildExit(t *testing.T) {
	t.Setenv("ASTRAL_TEST_LOCK_CHILD", "1")
	data, computers, daemon := maintenanceLockFixture(t)
	excluded, err := platform.Lock(filepath.Join(computers, "agents", "home", "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer excluded.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	marker := filepath.Join(t.TempDir(), "ready.json")
	result := make(chan error, 1)
	args := maintenanceChildArguments(t, data, computers, "wait", marker)
	go func() {
		result <- maintenanceWithDataLock(ctx, args, io.Discard, io.Discard)
	}()
	timeout := time.After(8 * time.Second)
	var raw []byte
	for raw == nil {
		select {
		case err := <-result:
			t.Fatalf("maintenance exited before child was ready: %v", err)
		case <-timeout:
			t.Fatal("maintenance child did not start")
		default:
			if bytes, err := os.ReadFile(marker); err == nil {
				raw = bytes
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if raw == nil {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
	var environment map[string]string
	if err = json.Unmarshal(raw, &environment); err != nil {
		t.Fatal(err)
	}
	expectedData, _ := filepath.EvalSymlinks(data)
	expectedComputers, _ := filepath.EvalSymlinks(computers)
	if environment["pid"] != strconv.Itoa(os.Getpid()) || environment["ppid"] != environment["pid"] || environment["data"] != expectedData || environment["computers"] != expectedComputers {
		t.Fatalf("child has incorrect lock identity: %+v", environment)
	}
	for _, file := range []string{filepath.Join(data, "server.lock"), daemon} {
		lock, err := platform.Lock(file)
		if err == nil {
			lock.Close()
			t.Fatalf("maintenance child did not retain %s", file)
		}
	}
	if _, err = os.Stat(filepath.Join(data, "astral.db")); !os.IsNotExist(err) {
		t.Fatalf("maintenance opened a database: %v", err)
	}
	cancel()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cancelled child did not exit before lock release")
	}
	for _, file := range []string{filepath.Join(data, "server.lock"), daemon} {
		lock, err := platform.Lock(file)
		if err != nil {
			t.Fatalf("lock not released after child exit: %v", err)
		}
		lock.Close()
	}
}
func TestMaintenanceWithDataLockRejectsLiveProcessAndReleasesAfterFailure(t *testing.T) {
	t.Setenv("ASTRAL_TEST_LOCK_CHILD", "1")
	for _, heldName := range []string{"server", "daemon"} {
		t.Run(heldName, func(t *testing.T) {
			data, computers, daemon := maintenanceLockFixture(t)
			heldPath := daemon
			if heldName == "server" {
				heldPath = filepath.Join(data, "server.lock")
			}
			held, err := platform.Lock(heldPath)
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "ready.json")
			args := maintenanceChildArguments(t, data, computers, "fail", marker)
			var output bytes.Buffer
			err = maintenanceWithDataLock(context.Background(), args, &output, &output)
			if err == nil {
				t.Fatal("maintenance accepted a live data owner")
			}
			if _, err = os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("child started before acquiring all locks")
			}
			held.Close()
			if err = maintenanceWithDataLock(context.Background(), args, &output, &output); err == nil {
				t.Fatal("child failure was suppressed")
			}
			if _, err = os.Stat(marker); err != nil {
				t.Fatal("child did not run after contention cleared")
			}
			for _, file := range []string{filepath.Join(data, "server.lock"), daemon} {
				lock, err := platform.Lock(file)
				if err != nil {
					t.Fatalf("failed child leaked process lock: %v", err)
				}
				lock.Close()
			}
		})
	}
}

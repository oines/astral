package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func maintenanceMarkerReadRetry(err error) bool {
	// The producer already closes a pending file before atomic rename. Windows
	// can still expose its name while a rename/scanner holds a conflicting share
	// mode. Only that exact transient error is eligible for the bounded retry.
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

func TestMaintenanceMarkerWaitRetriesActualWindowsSharingViolation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ready.json")
	want, err := json.Marshal(map[string]string{"pid": "123", "data": "ready"})
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(marker)
	if err != nil {
		t.Fatal(err)
	}
	// Create and retain the exclusive handle in one operation, so constructing
	// the fixture does not depend on reacquiring a file after a scanner opens it.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_ = windows.CloseHandle(handle)
		}
	}()
	var written uint32
	if err := windows.WriteFile(handle, want, &written, nil); err != nil {
		t.Fatal(err)
	}
	if int(written) != len(want) {
		t.Fatalf("incomplete ready fixture: wrote %d/%d bytes", written, len(want))
	}
	if _, err := os.ReadFile(marker); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || !maintenanceMarkerReadRetry(err) {
		t.Fatalf("fixture did not reproduce the native CI error: %v", err)
	}
	if maintenanceMarkerReadRetry(&os.PathError{Op: "open", Path: marker, Err: windows.ERROR_ACCESS_DENIED}) {
		t.Fatal("permanent access denial was classified as a transient share violation")
	}
	type result struct {
		raw []byte
		err error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan result, 1)
	go func() { raw, err := waitMaintenanceMarker(ctx, marker, make(chan error)); done <- result{raw, err} }()
	select {
	case early := <-done:
		t.Fatalf("ready read did not wait for the conflicting handle: %v", early.err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	locked = false
	select {
	case got := <-done:
		if got.err != nil || string(got.raw) != string(want) {
			t.Fatalf("ready read did not recover after share violation: %s, %v", got.raw, got.err)
		}
	case <-ctx.Done():
		t.Fatal("ready read did not finish after releasing the conflicting handle")
	}
}

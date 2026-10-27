package platform

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/releasefeed"
)

func TestContainerSupervisorPreservesEnvironmentAndRecoversFailedRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux container process lifecycle uses Unix signals")
	}
	for _, scenario := range []string{"success", "broken", "tampered-after-manifest"} {
		t.Run(scenario, func(t *testing.T) {
			broken := scenario == "broken"
			tampered := scenario == "tampered-after-manifest"
			directory := t.TempDir()
			root := filepath.Join(directory, "volume")
			programs := filepath.Join(directory, "usr-local-bin")
			if err := os.MkdirAll(programs, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "agents"), 0700); err != nil {
				t.Fatal(err)
			}
			userFile := filepath.Join(root, "agents", "keep.txt")
			_ = os.WriteFile(userFile, []byte("agent environment"), 0600)
			marker := filepath.Join(directory, "process-state")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			script := func(version string) []byte {
				return []byte("#!/bin/sh\ntrap 'exit 0' INT TERM\nprintf '%s %s\\n' " + quote(version) + " \"$$\" > " + quote(marker) + "\nwhile :; do sleep 1; done\n")
			}
			paths := containerSupervisorPaths{binary: filepath.Join(programs, "astral"), backup: filepath.Join(programs, ".astral-previous"), request: filepath.Join(programs, "update.json"), reconnectTimeout: 500 * time.Millisecond}
			if scenario == "success" {
				// Allow the real shell startup and TLS status polling to complete
				// under race instrumentation and concurrent package tests.
				paths.reconnectTimeout = 3 * time.Second
			}
			paths.verify = func(context.Context, releasefeed.Binary) error {
				if !tampered {
					return nil
				}
				entries, err := os.ReadDir(programs)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".astral-release-") {
						return os.WriteFile(filepath.Join(programs, entry.Name()), []byte("malicious replacement"), 0700)
					}
				}
				return fmt.Errorf("staged executable is missing")
			}
			old := script("0.1.0")
			_ = os.WriteFile(paths.binary, old, 0700)
			_, credentials := secureStatusFixture(t, func() domain.ComputerStatus {
				raw, _ := os.ReadFile(marker)
				fields := strings.Fields(string(raw))
				status := domain.ComputerStatus{}
				if len(fields) == 2 {
					status.Online = true
					status.BootID = fields[1]
					status.DaemonVersion = fields[0]
				}
				return status
			})
			if err := WriteJSON(filepath.Join(root, "credentials.json"), credentials); err != nil {
				t.Fatal(err)
			}
			originalCredentials, _ := os.ReadFile(filepath.Join(root, "credentials.json"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- superviseContainer(ctx, root, "", io.Discard, io.Discard, paths) }()
			wait := func(predicate func() bool) {
				t.Helper()
				deadline := time.Now().Add(8 * time.Second)
				for time.Now().Before(deadline) {
					if predicate() {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("supervisor condition did not complete")
			}
			var previousBoot string
			wait(func() bool {
				raw, _ := os.ReadFile(marker)
				fields := strings.Fields(string(raw))
				if len(fields) == 2 {
					previousBoot = fields[1]
					return true
				}
				return false
			})
			next := script("0.1.1")
			if broken {
				next = []byte("#!/bin/sh\nexit 17\n")
			}
			hash := sha256.Sum256(next)
			descriptor := releasefeed.Binary{Version: "0.1.1", Name: "astral-linux-" + runtime.GOARCH, SHA256: fmt.Sprintf("%x", hash)}
			stage := filepath.Join(programs, ".astral-release-"+descriptor.SHA256)
			_ = os.WriteFile(stage, next, 0700)
			if err := WriteJSON(paths.request, containerUpdate{Binary: descriptor, Path: stage, PreviousBoot: previousBoot}); err != nil {
				t.Fatal(err)
			}
			wait(func() bool {
				_, err := os.Stat(paths.request)
				if !os.IsNotExist(err) {
					return false
				}
				raw, _ := os.ReadFile(marker)
				fields := strings.Fields(string(raw))
				return len(fields) == 2 && fields[1] != previousBoot
			})
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("supervisor did not stop its child")
			}
			raw, _ := os.ReadFile(paths.binary)
			want := next
			if broken || tampered {
				want = old
			}
			if string(raw) != string(want) {
				t.Fatal("working program was not committed or restored")
			}
			kept, _ := os.ReadFile(userFile)
			savedCredentials, _ := os.ReadFile(filepath.Join(root, "credentials.json"))
			if string(kept) != "agent environment" || string(savedCredentials) != string(originalCredentials) {
				t.Fatal("update changed user data or enrolled identity")
			}
		})
	}
}

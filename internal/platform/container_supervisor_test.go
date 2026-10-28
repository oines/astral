package platform

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/releasefeed"
)

func TestContainerSupervisorPreservesEnvironmentAndRecoversFailedRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux container process lifecycle uses Unix signals")
	}
	for _, scenario := range []string{"success", "broken", "tampered-after-manifest", "manifest-rejected", "invalid-stage", "stale-boot", "not-newer", "cancelled-verification", "restart-failed"} {
		t.Run(scenario, func(t *testing.T) {
			broken := scenario == "broken"
			tampered := scenario == "tampered-after-manifest"
			rejected := scenario == "manifest-rejected" || scenario == "invalid-stage" || scenario == "stale-boot" || scenario == "not-newer"
			cancelled := scenario == "cancelled-verification"
			restartFailed := scenario == "restart-failed"
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
			if scenario == "success" || rejected {
				// Allow the real shell startup and TLS status polling to complete
				// under race instrumentation and concurrent package tests.
				paths.reconnectTimeout = 3 * time.Second
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var verifications atomic.Int64
			paths.verify = func(context.Context, releasefeed.Binary) error {
				attempt := verifications.Add(1)
				if cancelled {
					cancel()
					return ctx.Err()
				}
				if restartFailed {
					if err := os.Chmod(paths.binary, 0600); err != nil {
						return err
					}
					return errors.New("official manifest unavailable")
				}
				if scenario == "manifest-rejected" && attempt == 1 {
					return errors.New("official manifest unavailable")
				}
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
			request := containerUpdate{Binary: descriptor, Path: stage, PreviousBoot: previousBoot}
			switch scenario {
			case "invalid-stage":
				_ = os.WriteFile(stage, []byte("damaged download"), 0700)
			case "stale-boot":
				request.PreviousBoot = "disconnected-boot"
			case "not-newer":
				request.Binary.Version = "0.1.0"
			}
			if err := WriteJSON(paths.request, request); err != nil {
				t.Fatal(err)
			}
			wait(func() bool {
				_, err := os.Stat(paths.request)
				if !os.IsNotExist(err) {
					return false
				}
				if cancelled || restartFailed {
					return true
				}
				raw, _ := os.ReadFile(marker)
				fields := strings.Fields(string(raw))
				return len(fields) == 2 && fields[1] != previousBoot
			})
			if rejected {
				current, _ := os.ReadFile(paths.binary)
				state, _ := os.ReadFile(marker)
				fields := strings.Fields(string(state))
				if string(current) != string(old) || len(fields) != 2 || fields[0] != "0.1.0" {
					t.Fatal("rejection did not restart the unchanged previous program")
				}
				// A new old-version process has no in-memory update guard from the
				// failed attempt. Its next explicit request must be consumed normally.
				retriedBoot := fields[1]
				if err := os.WriteFile(stage, next, 0700); err != nil {
					t.Fatal(err)
				}
				if err := WriteJSON(paths.request, containerUpdate{Binary: descriptor, Path: stage, PreviousBoot: retriedBoot}); err != nil {
					t.Fatal(err)
				}
				wait(func() bool {
					_, err := os.Stat(paths.request)
					state, _ := os.ReadFile(marker)
					fields := strings.Fields(string(state))
					return os.IsNotExist(err) && len(fields) == 2 && fields[0] == "0.1.1" && fields[1] != retriedBoot
				})
				if scenario == "manifest-rejected" && verifications.Load() != 2 {
					t.Fatalf("retry did not recheck the manifest: %d checks", verifications.Load())
				}
			}
			// These two rejection paths must finish themselves, either by noticing
			// cancellation or reporting that the unchanged program cannot restart.
			if !cancelled && !restartFailed {
				cancel()
			}
			select {
			case err := <-done:
				if restartFailed && err == nil {
					t.Fatal("failed restart was reported as success")
				}
				if !restartFailed && err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("supervisor did not stop its child")
			}
			raw, _ := os.ReadFile(paths.binary)
			want := next
			if broken || tampered || cancelled || restartFailed {
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

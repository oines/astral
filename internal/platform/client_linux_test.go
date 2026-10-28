package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestLinuxMissingUserManagerFailsBeforePairingAndForegroundSkipsPreparation(t *testing.T) {
	bin := t.TempDir()
	// These fixtures replace all manager commands, including root preparation.
	// The test never talks to the machine's actual service manager.
	for _, name := range []string{"loginctl", "systemctl"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho 'fixture manager unavailable' >&2\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	server, err := newAuthenticatedTestServer(Config{DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())
	var requests atomic.Int32
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()
	ctx := context.Background()
	enrollment, err := server.Hub.Enroll(ctx, unavailable.URL, false, false)
	if err != nil {
		t.Fatal(err)
	}
	options := ConnectOptions{ServerURL: unavailable.URL, ServerPin: enrollment.ServerPin, EnrollmentID: enrollment.Enrollment.ID, Token: enrollment.Token, Root: filepath.Join(root, "remote")}
	err = Connect(ctx, options, nil)
	if err == nil || !strings.Contains(err.Error(), "prepare current-user Daemon service before pairing") {
		t.Fatalf("service preparation must fail before registration: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("failed service preparation contacted the registration endpoint")
	}
	var saved domain.Enrollment
	if err := server.Store.Get(ctx, "enrollments", enrollment.Enrollment.ID, &saved); err != nil || saved.State != "waiting" || saved.ComputerID != "" {
		t.Fatalf("failed preparation consumed the enrollment: %+v %v", saved, err)
	}
	if _, err := os.Stat(options.Root); !os.IsNotExist(err) {
		t.Fatalf("failed service preflight created Daemon state: %v", err)
	}
	options.Foreground = true
	err = Connect(ctx, options, nil)
	if err == nil || strings.Contains(err.Error(), "prepare current-user") || requests.Load() != 1 {
		t.Fatalf("foreground mode must reach enrollment without user-service preparation: %v", err)
	}
}

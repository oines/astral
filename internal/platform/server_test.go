package platform

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpapi"
)

func await(t *testing.T, ctx context.Context, check func() bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !check() {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}
func TestLocalComputerReuseAndRemoteEnrollmentUseRealDaemonConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	config := Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers")}
	s, err := newAuthenticatedTestServer(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	local, err := s.UseLocal(ctx)
	if err != nil || !local.Online || !local.Local || local.Kind != "local" {
		t.Fatalf("local shortcut did not connect: %+v %v", local, err)
	}
	again, err := s.UseLocal(ctx)
	if err != nil || again.ID != local.ID {
		t.Fatal("local shortcut duplicated a Computer")
	}
	if _, err = newAuthenticatedTestServer(config); err == nil {
		t.Fatal("two Servers opened the same state domain")
	}
	var bootstrap httpapi.Bootstrap
	if err = desktopJSON(ctx, "GET", s.URL+"/api/v1/bootstrap", testDesktopToken, nil, &bootstrap); err != nil {
		t.Fatal(err)
	}
	if bootstrap.Host.Hostname == "" || bootstrap.Host.OS == "" || len(bootstrap.Computers) != 1 {
		t.Fatal("bootstrap confused Server host with browser")
	}
	assertPrivate(t, filepath.Join(s.Config.DataDirectory, "control.json"))
	enrollment, err := s.Hub.Enroll(ctx, s.URL, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enrollment.Command, "/install-computer.sh") || !strings.Contains(enrollment.Command, enrollment.Enrollment.ID) {
		t.Fatal("enrollment did not produce an installer command")
	}
	remoteCtx, stopRemote := context.WithCancel(ctx)
	done := make(chan error, 1)
	options := ConnectOptions{ServerURL: s.URL, ServerPin: s.Hub.ServerPin(), EnrollmentID: enrollment.Enrollment.ID, Token: enrollment.Token, Root: filepath.Join(root, "remote"), Foreground: true}
	go func() { done <- Connect(remoteCtx, options, nil) }()
	var remote domain.Computer
	await(t, ctx, func() bool {
		var e domain.Enrollment
		if s.Store.Get(ctx, "enrollments", enrollment.Enrollment.ID, &e) != nil || e.ComputerID == "" {
			return false
		}
		return s.Store.Get(ctx, "computers", e.ComputerID, &remote) == nil && s.Hub.Status(remote).Online
	})
	if remote.ID == local.ID || remote.Kind != "remote" {
		t.Fatal("remote enrollment replaced local identity")
	}
	previousBoot := s.Hub.Status(remote).BootID
	stopRemote()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	remoteCtx, stopRemote = context.WithCancel(ctx)
	go func() { done <- Connect(remoteCtx, options, nil) }()
	await(t, ctx, func() bool { status := s.Hub.Status(remote); return status.Online && status.BootID != previousBoot })
	stopRemote()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	computers, err := s.Store.List(ctx, "computers")
	if err != nil || len(computers) != 2 {
		t.Fatal("retry created another Computer")
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s2, err := newAuthenticatedTestServer(config)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close(context.Background())
	if err = s2.Start(); err != nil {
		t.Fatal(err)
	}
	reopened, err := s2.UseLocal(ctx)
	if err != nil || reopened.ID != local.ID || !reopened.Online {
		t.Fatal("Server restart lost local Computer identity")
	}
}

func TestContainerFailureIsDurableAndRequestRetryDoesNotDuplicate(t *testing.T) {
	// An unavailable executable is a real supported failure mode, not a mocked
	// successful container. No Computer is registered by the failed operation.
	t.Setenv("PATH", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	s, err := newAuthenticatedTestServer(Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers"), NoLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	operation, err := s.CreateContainer(ctx, "retryable-create")
	if err != nil {
		t.Fatal(err)
	}
	await(t, ctx, func() bool {
		return s.Store.Get(ctx, "provisioning", operation.ID, &operation) == nil && operation.State == "failed"
	})
	if operation.ComputerID != "" || !strings.Contains(operation.Error, "container") {
		t.Fatalf("failure invented a Computer: %+v", operation)
	}
	s.mu.Lock()
	active := s.provisioning[operation.ID]
	s.mu.Unlock()
	if active {
		await(t, ctx, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.provisioning[operation.ID] })
	}
	retry, err := s.CreateContainer(ctx, "retryable-create")
	if err != nil || retry.ID != operation.ID {
		t.Fatal("same request created another operation")
	}
	if all, err := s.Store.List(ctx, "computers"); err != nil || len(all) != 0 {
		t.Fatal("unavailable runtime registered an online placeholder")
	}
	response, err := http.Get(s.URL + "/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("Server still serves an untrusted installer endpoint")
	}
}

func TestRestoreKnownLocalContainerMetadataWithoutRuntimeAccess(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := newAuthenticatedTestServer(Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers"), NoLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	known := domain.Computer{Meta: domain.Meta{ID: "known"}, Name: "User name", Kind: "container"}
	unknown := domain.Computer{Meta: domain.Meta{ID: "unknown"}, Name: "Remote container", Kind: "container"}
	for _, computer := range []*domain.Computer{&known, &unknown} {
		if err = s.Store.Save(ctx, "computers", &computer.Meta, computer); err != nil {
			t.Fatal(err)
		}
	}
	operation := domain.Provisioning{Meta: domain.Meta{ID: "container_original"}, Kind: "container", State: "connected", ComputerID: known.ID}
	if err = s.Store.Save(ctx, "provisioning", &operation.Meta, &operation); err != nil {
		t.Fatal(err)
	}
	// A machine with no Docker executable can recover stored identities.
	t.Setenv("PATH", t.TempDir())
	if err = s.restoreContainerMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Get(ctx, "computers", known.ID, &known); err != nil {
		t.Fatal(err)
	}
	if known.Name != "User name" || known.Container == nil || known.Container.Name != "astral-container_original" || known.Container.Volume != "astral-container_original-data" || known.Container.Host != "" || known.Container.Image != "" {
		t.Fatalf("incorrect legacy metadata: %+v", known)
	}
	if err = s.Store.Get(ctx, "computers", unknown.ID, &unknown); err != nil || unknown.Container != nil {
		t.Fatal("remote container metadata was invented")
	}
	revision := known.Revision
	if err = s.restoreContainerMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Get(ctx, "computers", known.ID, &known); err != nil || known.Revision != revision {
		t.Fatal("restoring metadata repeatedly changed the Computer")
	}
}

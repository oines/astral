package computers_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
)

func TestDaemonReportsCreatorSuppliedContainerIdentity(t *testing.T) {
	info := domain.ContainerInfo{Name: "astral-example", Host: "build-machine", Image: "astral-computer:example", Volume: "astral-example-data", MountPath: "/var/lib/astral"}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASTRAL_CONTAINER_INFO", string(raw))
	root := t.TempDir()
	d, err := computers.NewDaemon(root, computers.Credentials{ServerURL: "http://127.0.0.1:4545"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Hello.Root != root || d.Hello.Container == nil || *d.Hello.Container != info {
		t.Fatalf("daemon lost its data root or creator-supplied identity: %+v", d.Hello)
	}
}

func TestReportedEnvironmentPersistsAcrossDisconnectAndLegacyReconnect(t *testing.T) {
	s := fixture(t, 1)
	d, err := computers.NewDaemon(t.TempDir(), computers.Credentials{ServerURL: s.server.URL})
	if err != nil {
		t.Fatal(err)
	}
	info := domain.ContainerInfo{Name: "astral-persistent", Host: "remote-host", Image: "astral-computer:one", Volume: "astral-persistent-data", MountPath: "/var/lib/astral"}
	d.Hello.Container = &info
	e, err := s.hub.Enroll(s.ctx, s.server.URL, true, false)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := s.hub.Register(s.ctx, e.Enrollment.ID, computers.EnrollmentRequest{Token: e.Token, Hello: d.Hello})
	if err != nil {
		t.Fatal(err)
	}
	credentials.ServerURL = s.server.URL
	d.Credentials = credentials
	var record domain.Computer
	if err = s.store.Get(s.ctx, "computers", credentials.ComputerID, &record); err != nil {
		t.Fatal(err)
	}
	record.Name = "My build container"
	if err = s.store.Save(s.ctx, "computers", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	// Connection reports can refresh metadata, while preserving a user's name.
	d.Hello.Root = filepath.Join(d.Root, "data")
	d.Hello.Container.Image = "astral-computer:two"
	s.daemons = append(s.daemons, d)
	go func() { _ = d.Connect(s.ctx) }()
	waitOnline(t, s, credentials.ComputerID, d.Hello.BootID)
	if err = s.store.Get(s.ctx, "computers", credentials.ComputerID, &record); err != nil {
		t.Fatal(err)
	}
	if record.Root != d.Hello.Root || record.Name != "My build container" || record.Container == nil || record.Container.Image != "astral-computer:two" {
		t.Fatalf("latest environment was not persisted or overwrote the name: %+v", record)
	}
	_ = d.Close(context.Background())
	for s.hub.Status(record).Online {
		select {
		case <-s.ctx.Done():
			t.Fatal(s.ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if status := s.hub.Status(record); status.Container == nil || status.Container.Name != info.Name || status.Root != d.Hello.Root {
		t.Fatal("offline status lost the reported environment")
	}
	legacy, err := computers.NewDaemon(t.TempDir(), credentials)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Hello.Root, legacy.Hello.Container = "", nil
	s.daemons = append(s.daemons, legacy)
	go func() { _ = legacy.Connect(s.ctx) }()
	waitOnline(t, s, credentials.ComputerID, legacy.Hello.BootID)
	if err = s.store.Get(s.ctx, "computers", credentials.ComputerID, &record); err != nil {
		t.Fatal(err)
	}
	if status := s.hub.Status(record); status.Container == nil || status.Container.Name != info.Name || status.Root != d.Hello.Root || status.Name != "My build container" {
		t.Fatal("an older Daemon cleared previously reported metadata")
	}
	var agent domain.Agent
	if err = s.store.Get(s.ctx, "agents", s.agent.ID, &agent); err != nil || len(agent.AllowedComputers) != 1 {
		t.Fatal("environment reporting changed Computer permissions")
	}
}

func TestContainerMetadataDoesNotChangeComputerKind(t *testing.T) {
	s := fixture(t, 1)
	hello := s.daemons[0].Hello
	hello.Container = &domain.ContainerInfo{Name: "not-a-container"}
	e, err := s.hub.Enroll(s.ctx, s.server.URL, false, false)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := s.hub.Register(s.ctx, e.Enrollment.ID, computers.EnrollmentRequest{Token: e.Token, Hello: hello})
	if err != nil {
		t.Fatal(err)
	}
	var record domain.Computer
	if err := s.store.Get(s.ctx, "computers", credentials.ComputerID, &record); err != nil {
		t.Fatal(err)
	}
	if status := s.hub.Status(record); status.Kind != "remote" || status.Container != nil {
		t.Fatal("reported metadata changed the registered Computer kind")
	}
}

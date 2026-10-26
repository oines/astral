package computers

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/releasefeed"
	"github.com/oines/astral/internal/transport"
)

func updateFixture(t *testing.T) (*Hub, *Daemon, *connection) {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHub(store)
	d, err := NewDaemon(t.TempDir(), Credentials{ServerURL: "http://127.0.0.1:4545", ComputerID: "computer-update"})
	if err != nil {
		t.Fatal(err)
	}
	d.Hello.Version = "0.1.0"
	d.Hello.Capabilities = append(d.Hello.Capabilities, DaemonUpdateCapability)
	c := domain.Computer{Meta: domain.Meta{ID: d.Credentials.ComputerID}, Kind: "remote", DaemonVersion: d.Hello.Version}
	if err = store.Save(context.Background(), "computers", &c.Meta, &c); err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	generation := &daemonConnection{executions: map[string]string{}}
	generation.peer = transport.New(a, func(ctx context.Context, r transport.Request, out *transport.Responder) error {
		return d.handle(ctx, generation, r, out)
	})
	d.connection, d.peer = generation, generation.peer
	active := &connection{peer: transport.New(b, nil), hello: d.Hello, connected: time.Now()}
	h.live[c.ID] = active
	name, _ := releasefeed.AssetName(d.Hello.OS, d.Hello.Arch)
	h.updates.latest = func(context.Context, string, string) (releasefeed.Binary, error) {
		return releasefeed.Binary{Version: "0.1.1", Name: name, SHA256: strings.Repeat("a", 64)}, nil
	}
	h.updates.reconnectTimeout = 100 * time.Millisecond
	t.Cleanup(func() { _ = d.Close(context.Background()); _ = h.Close(); _ = store.Close() })
	return h, d, active
}

func waitUpdateState(t *testing.T, h *Hub, id, state string) domain.DaemonUpdate {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		value := h.DaemonUpdateStatus(id)
		if value.State == state {
			return value
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("update did not reach %s: %+v", state, h.DaemonUpdateStatus(id))
	return domain.DaemonUpdate{}
}

func TestDaemonUpdateDeduplicatesAndRequiresNewBootAndVersion(t *testing.T) {
	h, d, original := updateFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int64
	d.Update = func(ctx context.Context, b releasefeed.Binary) error {
		count.Add(1)
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	id := d.Credentials.ComputerID
	if _, err := h.UpdateDaemon(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	<-started
	for range 3 {
		if _, err := h.UpdateDaemon(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if count.Load() != 1 {
		t.Fatal("duplicate update dispatch")
	}
	// A new connection reporting the old version is not a successful update.
	next := *original
	next.hello.BootID = "new-boot"
	next.hello.Version = "0.1.0"
	h.mu.Lock()
	h.live[id] = &next
	h.mu.Unlock()
	close(release)
	value := waitUpdateState(t, h, id, "failed")
	if value.Error == "" {
		t.Fatal("failed reconnection lost its explanation")
	}
}

func TestDaemonUpdateAcceptsHandoffDisconnectAndVerifiesReconnection(t *testing.T) {
	h, d, original := updateFixture(t)
	id := d.Credentials.ComputerID
	d.Update = func(context.Context, releasefeed.Binary) error {
		next := *original
		next.hello.BootID = "new-boot"
		next.hello.Version = "0.1.1"
		h.mu.Lock()
		h.live[id] = &next
		h.mu.Unlock()
		_ = original.peer.Close()
		return nil
	}
	if _, err := h.UpdateDaemon(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	value := waitUpdateState(t, h, id, "complete")
	if value.Version != "0.1.1" {
		t.Fatal("completion did not report expected version")
	}
}

func TestDaemonUpdateCurrentVersionAndMissingManagementCapability(t *testing.T) {
	h, d, active := updateFixture(t)
	id := d.Credentials.ComputerID
	name, _ := releasefeed.AssetName(d.Hello.OS, d.Hello.Arch)
	h.updates.latest = func(context.Context, string, string) (releasefeed.Binary, error) {
		return releasefeed.Binary{Version: "0.1.0", Name: name, SHA256: strings.Repeat("a", 64)}, nil
	}
	if _, err := h.UpdateDaemon(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	waitUpdateState(t, h, id, "current")
	active.hello.Capabilities = nil
	if _, err := h.UpdateDaemon(context.Background(), id); err == nil {
		t.Fatal("accepted unmanaged update")
	}
	var c domain.Computer
	if err := h.Store.Get(context.Background(), "computers", id, &c); err != nil {
		t.Fatal(err)
	}
	c.Revoked = true
	if err := h.Store.Save(context.Background(), "computers", &c.Meta, &c); err != nil {
		t.Fatal(err)
	}
	if _, err := h.UpdateDaemon(context.Background(), id); err == nil {
		t.Fatal("updated revoked Computer")
	}
}

func TestDaemonUpdateDownloadCancelsWhenTunnelDisconnects(t *testing.T) {
	h, d, original := updateFixture(t)
	started, cancelled := make(chan struct{}), make(chan struct{})
	d.Update = func(ctx context.Context, _ releasefeed.Binary) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}
	if _, err := h.UpdateDaemon(context.Background(), d.Credentials.ComputerID); err != nil {
		t.Fatal(err)
	}
	<-started
	_ = original.peer.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disconnected download retained its RPC context")
	}
	waitUpdateState(t, h, d.Credentials.ComputerID, "failed")
}

func TestDaemonUpdateDoesNotDowngradeToOlderLatestRelease(t *testing.T) {
	h, d, _ := updateFixture(t)
	name, _ := releasefeed.AssetName(d.Hello.OS, d.Hello.Arch)
	h.updates.latest = func(context.Context, string, string) (releasefeed.Binary, error) {
		return releasefeed.Binary{Version: "0.0.9", Name: name, SHA256: strings.Repeat("a", 64)}, nil
	}
	d.Update = func(context.Context, releasefeed.Binary) error {
		t.Error("older latest release attempted an install")
		return nil
	}
	if _, err := h.UpdateDaemon(context.Background(), d.Credentials.ComputerID); err != nil {
		t.Fatal(err)
	}
	state := waitUpdateState(t, h, d.Credentials.ComputerID, "current")
	if state.Version != "0.1.0" {
		t.Fatal("no-op must report the installed version")
	}
}

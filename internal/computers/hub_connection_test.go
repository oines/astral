package computers

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/transport"
)

func connectionOrderFixture(t *testing.T) (*Hub, domain.Computer) {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(store)
	t.Cleanup(func() { _ = hub.Close(); _ = store.Close() })
	computer := domain.Computer{Meta: domain.Meta{ID: domain.ID("computer")}, Name: "test"}
	if err = store.Save(t.Context(), "computers", &computer.Meta, &computer); err != nil {
		t.Fatal(err)
	}
	return hub, computer
}

// Calling serveConnection directly lets the tests control the scheduling gap
// after an authentication acknowledgement without adding a production hook.
func serveOrderedConnection(t *testing.T, hub *Hub, id, boot string, sequence uint64) (*transport.Peer, <-chan struct{}) {
	t.Helper()
	server, client := net.Pipe()
	remote := transport.New(client, func(_ context.Context, _ transport.Request, out *transport.Responder) error {
		return out.JSON(boot)
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.serveConnection(t.Context(), server, id, Hello{BootID: boot}, sequence)
	}()
	t.Cleanup(func() {
		_ = remote.Close()
		waitConnectionExit(t, done)
	})
	return remote, done
}

func waitConnectionExit(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func waitOrderedConnection(t *testing.T, hub *Hub, id, boot string, done <-chan struct{}) *connection {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		hub.mu.RLock()
		active := hub.live[id]
		hub.mu.RUnlock()
		if active != nil && active.hello.BootID == boot {
			return active
		}
		select {
		case <-done:
			t.Fatal("server connection exited before admission")
		case <-ctx.Done():
			t.Fatal("server connection was not admitted")
		case <-time.After(time.Millisecond):
		}
	}
}

func requireConnectionUsable(t *testing.T, active *connection) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var boot string
	if err := active.peer.JSON(ctx, "probe", nil, &boot); err != nil {
		t.Fatalf("admitted connection is unusable: %v", err)
	}
	if boot != active.hello.BootID {
		t.Fatalf("probe reached boot %q, want %q", boot, active.hello.BootID)
	}
}

func TestHubLateConnectionCannotReplaceNewerConnection(t *testing.T) {
	hub, computer := connectionOrderFixture(t)
	oldSequence := hub.reserveConnectionSequence()
	newSequence := hub.reserveConnectionSequence()
	_, currentDone := serveOrderedConnection(t, hub, computer.ID, "new", newSequence)
	current := waitOrderedConnection(t, hub, computer.ID, "new", currentDone)

	// The older handler resumes only after the newer connection is usable.
	stale, staleDone := serveOrderedConnection(t, hub, computer.ID, "old", oldSequence)
	waitConnectionExit(t, staleDone)
	select {
	case <-stale.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("rejected older connection was not closed")
	}
	hub.mu.RLock()
	active := hub.live[computer.ID]
	hub.mu.RUnlock()
	if active != current {
		t.Fatal("late older connection replaced the newer connection")
	}
	requireConnectionUsable(t, current)
}

func TestHubLateConnectionCannotReturnAfterNewerConnectionExits(t *testing.T) {
	hub, computer := connectionOrderFixture(t)
	oldSequence := hub.reserveConnectionSequence()
	newSequence := hub.reserveConnectionSequence()
	current, currentDone := serveOrderedConnection(t, hub, computer.ID, "new", newSequence)
	_ = waitOrderedConnection(t, hub, computer.ID, "new", currentDone)
	_ = current.Close()
	waitConnectionExit(t, currentDone)

	_, staleDone := serveOrderedConnection(t, hub, computer.ID, "old", oldSequence)
	waitConnectionExit(t, staleDone)
	hub.mu.RLock()
	active := hub.live[computer.ID]
	hub.mu.RUnlock()
	if active != nil {
		t.Fatal("late older connection became live after newer connection cleanup")
	}
}

func TestHubFailedAcknowledgementDoesNotSupersedeConnections(t *testing.T) {
	hub, computer := connectionOrderFixture(t)
	firstSequence := hub.reserveConnectionSequence()
	_, firstDone := serveOrderedConnection(t, hub, computer.ID, "first", firstSequence)
	first := waitOrderedConnection(t, hub, computer.ID, "first", firstDone)
	acknowledgedSequence := hub.reserveConnectionSequence()
	failedSequence := hub.reserveConnectionSequence()
	server, client := net.Pipe()
	_ = client.Close()
	defer server.Close()
	if err := writeAuthentication(server, tunnelResponse{}); err == nil {
		t.Fatal("authentication acknowledgement unexpectedly succeeded")
	}
	// Connect returns on the write error; its reserved sequence is never admitted.
	hub.mu.RLock()
	active, watermark := hub.live[computer.ID], hub.admittedConnection[computer.ID]
	hub.mu.RUnlock()
	if active != first || watermark != firstSequence || failedSequence <= acknowledgedSequence {
		t.Fatal("failed acknowledgement changed the admitted connection")
	}
	requireConnectionUsable(t, first)

	// A previously acknowledged candidate can still complete admission even
	// though a later candidate reserved a sequence before its failed write.
	_, secondDone := serveOrderedConnection(t, hub, computer.ID, "second", acknowledgedSequence)
	second := waitOrderedConnection(t, hub, computer.ID, "second", secondDone)
	waitConnectionExit(t, firstDone)
	requireConnectionUsable(t, second)
}

func TestHubConnectionAdmissionRechecksRegistration(t *testing.T) {
	for _, action := range []string{"revoke", "delete"} {
		t.Run(action, func(t *testing.T) {
			hub, computer := connectionOrderFixture(t)
			sequence := hub.reserveConnectionSequence()
			if action == "revoke" {
				if err := hub.Revoke(t.Context(), computer.ID); err != nil {
					t.Fatal(err)
				}
			} else if err := hub.Store.Update(t.Context(), func(tx *persistence.Tx) error {
				return tx.Delete(t.Context(), "computers", computer.ID, computer.Revision)
			}); err != nil {
				t.Fatal(err)
			}
			_, done := serveOrderedConnection(t, hub, computer.ID, "unavailable", sequence)
			waitConnectionExit(t, done)
			hub.mu.RLock()
			active, watermark := hub.live[computer.ID], hub.admittedConnection[computer.ID]
			hub.mu.RUnlock()
			if active != nil || watermark != 0 {
				t.Fatal("unavailable registration was admitted")
			}
		})
	}
}

func TestHubForgetRemovesWatermarkWithoutAdmittingDeletedConnection(t *testing.T) {
	hub, computer := connectionOrderFixture(t)
	sequence := hub.reserveConnectionSequence()
	_, currentDone := serveOrderedConnection(t, hub, computer.ID, "current", sequence)
	_ = waitOrderedConnection(t, hub, computer.ID, "current", currentDone)
	lateSequence := hub.reserveConnectionSequence()
	if err := hub.Store.Update(t.Context(), func(tx *persistence.Tx) error {
		return tx.Delete(t.Context(), "computers", computer.ID, computer.Revision)
	}); err != nil {
		t.Fatal(err)
	}
	hub.Forget(computer.ID)
	waitConnectionExit(t, currentDone)
	_, lateDone := serveOrderedConnection(t, hub, computer.ID, "late", lateSequence)
	waitConnectionExit(t, lateDone)
	hub.mu.RLock()
	active := hub.live[computer.ID]
	_, retained := hub.admittedConnection[computer.ID]
	hub.mu.RUnlock()
	if active != nil || retained {
		t.Fatal("deleted computer retained connection state or admitted a late connection")
	}
}

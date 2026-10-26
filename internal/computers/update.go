package computers

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/releasefeed"
	"github.com/oines/astral/internal/transport"
)

const DaemonUpdateCapability = "management.update"

type daemonUpdates struct {
	sync.Mutex
	values           map[string]domain.DaemonUpdate
	latest           func(context.Context, string, string) (releasefeed.Binary, error)
	cancel           map[string]context.CancelFunc
	contexts         map[string]context.Context
	closed           bool
	reconnectTimeout time.Duration
}

// DaemonUpdateStatus is transient. Reconnected Hello metadata remains the
// authoritative installed version after a Server restart.
func (h *Hub) DaemonUpdateStatus(id string) domain.DaemonUpdate {
	h.updates.Lock()
	defer h.updates.Unlock()
	return h.updates.values[id]
}

func (h *Hub) CanUpdateDaemon(id string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	active := h.live[id]
	return active != nil && slices.Contains(active.hello.Capabilities, DaemonUpdateCapability)
}

// UpdateDaemon is exposed only on the authenticated Owner API, not the Agent
// gateway. The Server resolves latest itself; clients cannot supply executable
// URLs, checksums, versions, credentials or an execution binding.
func (h *Hub) UpdateDaemon(ctx context.Context, id string) (domain.DaemonUpdate, error) {
	if err := ctx.Err(); err != nil {
		return domain.DaemonUpdate{}, err
	}
	var computer domain.Computer
	if err := h.Store.Get(ctx, "computers", id, &computer); err != nil {
		return domain.DaemonUpdate{}, err
	}
	if computer.Local || computer.Kind == "local" {
		return domain.DaemonUpdate{}, domain.Conflict("the local Daemon updates with Desktop or Server")
	}
	if computer.Revoked {
		return domain.DaemonUpdate{}, domain.Forbidden("Computer is revoked")
	}
	h.mu.RLock()
	active := h.live[id]
	h.mu.RUnlock()
	if active == nil {
		return domain.DaemonUpdate{}, domain.Offline("Computer is offline")
	}
	if !slices.Contains(active.hello.Capabilities, DaemonUpdateCapability) {
		return domain.DaemonUpdate{}, domain.Conflict("use the update command to install a Daemon with managed updates")
	}
	h.updates.Lock()
	if h.updates.closed {
		h.updates.Unlock()
		return domain.DaemonUpdate{}, domain.Offline("Server is stopping")
	}
	if h.updates.values == nil {
		h.updates.values = map[string]domain.DaemonUpdate{}
	}
	previous := h.updates.values[id]
	if previous.State == "downloading" || previous.State == "restarting" || previous.State == "checking" {
		h.updates.Unlock()
		return previous, nil
	}
	operation := domain.DaemonUpdate{State: "checking", StartedAt: time.Now().UTC()}
	h.updates.values[id] = operation
	if h.updates.cancel == nil {
		h.updates.cancel = map[string]context.CancelFunc{}
		h.updates.contexts = map[string]context.Context{}
	}
	updateCtx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	h.updates.cancel[id] = cancel
	h.updates.contexts[id] = updateCtx
	h.updates.Unlock()
	// Managed update ownership outlives an HTTP request/tab closure, while the
	// bounded request to the old Daemon still cancels on tunnel disconnection.
	go h.runDaemonUpdate(updateCtx, cancel, id, active)
	return operation, nil
}

func (h *Hub) setDaemonUpdate(id string, value domain.DaemonUpdate) {
	h.updates.Lock()
	defer h.updates.Unlock()
	h.updates.values[id] = value
}

func (h *Hub) runDaemonUpdate(ctx context.Context, cancel context.CancelFunc, id string, original *connection) {
	defer cancel()
	defer func() {
		h.updates.Lock()
		if h.updates.contexts[id] == ctx {
			delete(h.updates.cancel, id)
			delete(h.updates.contexts, id)
		}
		h.updates.Unlock()
	}()
	state := h.DaemonUpdateStatus(id)
	fail := func(err error) { state.State = "failed"; state.Error = err.Error(); h.setDaemonUpdate(id, state) }
	resolve := h.updates.latest
	if resolve == nil {
		resolve = releasefeed.Latest
	}
	binary, err := resolve(ctx, original.hello.OS, original.hello.Arch)
	if err != nil {
		fail(err)
		return
	}
	if comparison, e := releasefeed.Compare(binary.Version, original.hello.Version); e == nil && comparison <= 0 {
		state.State = "current"
		state.Version = original.hello.Version
		h.setDaemonUpdate(id, state)
		return
	}
	h.mu.RLock()
	active := h.live[id]
	h.mu.RUnlock()
	if active != original {
		fail(domain.Offline("Computer reconnected before update started; retry"))
		return
	}
	state.State = "downloading"
	state.Version = binary.Version
	h.setDaemonUpdate(id, state)
	err = original.peer.JSON(ctx, DaemonUpdateCapability, binary, &struct{}{})
	// Restart may close the old tunnel before its acknowledgement arrives.
	if err != nil && !errors.Is(err, transport.ErrDisconnected) && !errors.Is(err, io.EOF) {
		fail(err)
		return
	}
	state.State = "restarting"
	h.setDaemonUpdate(id, state)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	timeout := h.updates.reconnectTimeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	wait, stop := context.WithTimeout(ctx, timeout)
	defer stop()
	for {
		h.mu.RLock()
		next := h.live[id]
		h.mu.RUnlock()
		if next != nil && next.hello.BootID != original.hello.BootID && next.hello.Version == binary.Version {
			state.State = "complete"
			h.setDaemonUpdate(id, state)
			return
		}
		select {
		case <-wait.Done():
			fail(errors.New("updated Daemon did not reconnect with the expected version; inspect its update log or use the update command"))
			return
		case <-ticker.C:
		}
	}
}

func (h *Hub) cancelDaemonUpdates() {
	h.updates.Lock()
	defer h.updates.Unlock()
	h.updates.closed = true
	for _, cancel := range h.updates.cancel {
		cancel()
	}
}

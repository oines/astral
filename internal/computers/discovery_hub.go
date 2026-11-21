//go:build !daemon

package computers

import (
	"context"
	"slices"

	"github.com/oines/astral/internal/domain"
)

// Discover uses a boot-pinned authorized connection and an explicit capability.
// Old daemons are reported as unsupported, never scanned via the Server's disk.
func (h *Hub) Discover(ctx context.Context, b domain.ExecutionBinding) (EnvironmentDiscovery, error) {
	peer, err := h.Peer(ctx, b)
	if err != nil {
		return EnvironmentDiscovery{}, err
	}
	h.mu.RLock()
	active := h.live[b.ComputerID]
	supported := active != nil && active.peer == peer && slices.Contains(active.hello.Capabilities, EnvironmentDiscoveryCapability)
	h.mu.RUnlock()
	if !supported {
		result := UnavailableDiscovery("unsupported", "This Computer's daemon does not support environment discovery.")
		result.ComputerID, result.CWD = b.ComputerID, b.CWD
		return result, nil
	}
	var result EnvironmentDiscovery
	if err = peer.JSON(ctx, EnvironmentDiscoveryCapability, Call{Binding: b}, &result); err != nil {
		return EnvironmentDiscovery{}, err
	}
	if err = h.Validate(ctx, b); err != nil {
		return EnvironmentDiscovery{}, err
	}
	if result.ComputerID != b.ComputerID || result.CWD != b.CWD {
		return EnvironmentDiscovery{}, domain.Invalid("environment discovery returned a different execution binding")
	}
	return result, nil
}

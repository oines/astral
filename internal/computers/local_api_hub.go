//go:build !daemon

package computers

// ComputerEventURL returns the actual local entry point advertised by the live
// owning Daemon. It never substitutes the Server or another Computer's address.
func (h *Hub) ComputerEventURL(id string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if active := h.live[id]; active != nil {
		return active.hello.LocalAPIURL
	}
	return ""
}

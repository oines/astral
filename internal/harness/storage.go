package harness

// StorageBusy checks already-open runtimes without opening any Session.
func (s *Service) StorageBusy() bool {
	s.mu.Lock()
	all := make([]*runtime, 0, len(s.runtimes))
	for _, r := range s.runtimes {
		all = append(all, r)
	}
	s.mu.Unlock()
	for _, r := range all {
		select {
		case <-r.ready:
		default:
			return true
		}
		if r.storageBusy() {
			return true
		}
	}
	return false
}

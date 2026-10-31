package harness

import (
	"context"
	"github.com/oines/astral/internal/domain"
)

// CreateStorageReplacement gives one durable cleanup operation a stable target.
func (s *Service) CreateStorageReplacement(ctx context.Context, old domain.Session, id string) (domain.Session, error) {
	return s.createSession(ctx, old.AgentID, old.Title, id, "im", true, old.Private)
}

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
		if session := r.core.Load(); session != nil {
			view := session.Snapshot()
			if view.Running || view.Maintenance {
				return true
			}
		}
	}
	return false
}

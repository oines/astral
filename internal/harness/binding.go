package harness

import (
	"context"
	"slices"
	"time"

	core "github.com/oines/astral-core"
)

// WithStoppedSessions serializes a binding change with input admission. Cancel
// and wait without holding runtime locks: an executing tool may need that lock
// to finish. Recheck after locking so a concurrent Web input cannot start a run
// between the stop and the binding commit.
func (s *Service) WithStoppedSessions(ctx context.Context, ids []string, change func() error) error {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	var runtimes []*runtime
	for _, id := range ids {
		r, err := s.open(ctx, id)
		if err != nil {
			return err
		}
		runtimes = append(runtimes, r)
	}
	for {
		for _, r := range runtimes {
			r.core.Load().Cancel()
		}
		locked := 0
		idle := true
		for _, r := range runtimes {
			if !r.mu.TryLock() {
				idle = false
				break
			}
			locked++
			snapshot := r.core.Load().Snapshot()
			if snapshot.Running || snapshot.Maintenance {
				idle = false
				break
			}
		}
		if idle {
			err := change()
			if err == nil {
				for _, r := range runtimes {
					if err = r.syncBusinessConfig(ctx); err != nil {
						break
					}
					s.notify(r.id)
				}
			}
			for i := locked - 1; i >= 0; i-- {
				runtimes[i].mu.Unlock()
			}
			return err
		}
		for i := locked - 1; i >= 0; i-- {
			runtimes[i].mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// WithSession serializes a Host-side IM decision with Web/IM admission and
// binding changes, while allowing the active Core run to reach its boundary.
func (s *Service) WithSession(ctx context.Context, id string, apply func(*core.Session) error) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if snapshot := r.core.Load().Snapshot(); !snapshot.Running && !snapshot.Maintenance {
		if err = r.syncBusinessConfig(ctx); err != nil {
			return err
		}
	}
	if err = apply(r.core.Load()); err != nil {
		return err
	}
	s.notify(id)
	return nil
}

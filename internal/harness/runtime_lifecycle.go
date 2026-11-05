package harness

import (
	"context"

	core "github.com/oines/astral-core"
)

type runtimeCorePhase uint8

const (
	runtimeCoreActive runtimeCorePhase = iota
	runtimeCoreClosing
	runtimeCoreClosed
)

// Snapshot reads share only this short gate, never the Host admission lock.
// Core clears its execution window on Close; Snapshot requires that window.
func (r *runtime) snapshot() (core.SessionSnapshot, error) {
	r.snapshotMu.RLock()
	defer r.snapshotMu.RUnlock()
	session := r.core.Load()
	if r.corePhase != runtimeCoreActive || session == nil {
		return core.SessionSnapshot{}, core.ErrClosed
	}
	return session.Snapshot(), nil
}

func (r *runtime) readableCore() (*core.Session, error) {
	r.snapshotMu.RLock()
	defer r.snapshotMu.RUnlock()
	if r.corePhase != runtimeCoreActive || r.core.Load() == nil {
		return nil, core.ErrClosed
	}
	return r.core.Load(), nil
}

// retire blocks new readers and prevents a concurrent binding reopen from
// publishing another handle. Callers then drain existing admission callbacks.
func (r *runtime) retire() {
	r.snapshotMu.Lock()
	r.retired = true
	session := r.core.Load()
	if r.corePhase == runtimeCoreActive {
		r.corePhase = runtimeCoreClosing
	}
	r.snapshotMu.Unlock()
	// An admitted callback can hold mu while awaiting Core maintenance. Cancel
	// before draining that lock; Close will join it after callbacks release mu.
	if session != nil {
		session.Cancel()
	}
}

func (r *runtime) closeCore(ctx context.Context) error {
	return r.closeCoreForReopen(ctx, false)
}

func (r *runtime) closeCoreForReopen(ctx context.Context, reopening bool) error {
	r.snapshotMu.Lock()
	session := r.core.Load()
	if r.corePhase == runtimeCoreClosed || session == nil {
		r.snapshotMu.Unlock()
		return nil
	}
	r.corePhase = runtimeCoreClosing
	r.snapshotMu.Unlock()
	// Never hold the snapshot/admission gate while cancelled tools finish.
	if err := session.Close(ctx); err != nil {
		return err
	}
	r.snapshotMu.Lock()
	if !reopening {
		r.corePhase = runtimeCoreClosed
	}
	r.snapshotMu.Unlock()
	return nil
}

func (r *runtime) reopenCore(ctx context.Context, config core.Config) error {
	next, err := core.OpenSession(ctx, config, r.id)
	if err != nil {
		r.snapshotMu.Lock()
		r.corePhase = runtimeCoreClosed
		r.snapshotMu.Unlock()
		return err
	}
	return r.publishCore(ctx, next)
}

func (r *runtime) publishCore(ctx context.Context, session *core.Session) error {
	r.snapshotMu.Lock()
	if r.retired {
		r.snapshotMu.Unlock()
		_ = session.Close(ctx)
		return core.ErrClosed
	}
	r.core.Store(session)
	r.corePhase = runtimeCoreActive
	r.snapshotMu.Unlock()
	return nil
}

func (r *runtime) storageBusy() bool {
	r.snapshotMu.RLock()
	defer r.snapshotMu.RUnlock()
	if r.corePhase == runtimeCoreClosing {
		return true
	}
	if r.corePhase == runtimeCoreClosed || r.core.Load() == nil {
		return false
	}
	view := r.core.Load().Snapshot()
	return view.Running || view.Maintenance
}

// Snapshot returns an already initialized runtime's consistent execution view.
// It is safe inside WithSession: it does not reacquire the admission lock.
func (s *Service) Snapshot(ctx context.Context, id string) (core.SessionSnapshot, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return core.SessionSnapshot{}, err
	}
	return r.snapshot()
}

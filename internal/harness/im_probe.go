package harness

import core "github.com/oines/astral-core"

// IMProbeRecoveryPossible inspects only an already loaded Session. A cold or
// opening Session is left to the caller's durable pending-input preflight.
// This is a hint: recovery must recheck eligibility under WithSession.
func (s *Service) IMProbeRecoveryPossible(id string) (possible, loaded bool) {
	s.mu.Lock()
	r := s.runtimes[id]
	s.mu.Unlock()
	if r == nil {
		return false, false
	}
	select {
	case <-r.ready:
	default:
		return false, false
	}
	if r.openErr != nil {
		return false, false
	}
	snapshot, err := r.snapshot()
	if err != nil {
		return false, false
	}
	state := snapshot.State
	if snapshot.Running || snapshot.Maintenance || snapshot.Stopping || state.PendingInputs[core.InputSteer] == 0 || state.CurrentAttemptID != "" || !state.Branch.Complete() {
		return false, true
	}
	pending, err := s.ModelFallbackPending(s.ctx, id)
	return err == nil && !pending, true
}

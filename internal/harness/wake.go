package harness

import (
	"context"
	"errors"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/wake"
)

func (s *Service) wakeExternal(id string) {
	if id != "" && s.WakeEndpoints != nil {
		s.WakeEndpoints.WakeSession(id)
	}
}

// deliverWake hands one immutable batch to Core. Host owns only the bounded
// pre-admission buffer; after Accepted, the Core receipt and frozen requests are
// authoritative even when the caller no longer has the event payload.
func (s *Service) deliverWake(ctx context.Context, batch wake.Batch) (wake.DeliveryResult, error) {
	var record domain.Session
	if err := s.Store.Get(ctx, "sessions", batch.SessionID, &record); err != nil {
		return wake.DeliveryResult{}, err
	}
	if record.AgentID != batch.AgentID || record.Deletion != nil {
		return wake.DeliveryResult{}, domain.Missing("Session", batch.SessionID)
	}
	if record.SubagentParentID != "" {
		return wake.DeliveryResult{}, domain.Forbidden("external events cannot activate an internal subagent")
	}
	if err := persistence.CheckSessionRunnable(ctx, record, s.Store); err != nil {
		return wake.DeliveryResult{}, err
	}
	if batch.ID == "" || batch.Digest == "" {
		return wake.DeliveryResult{}, domain.Invalid("external event batch needs a stable identity")
	}
	r, err := s.open(ctx, batch.SessionID)
	if err != nil {
		return wake.DeliveryResult{}, err
	}
	// Maintenance can hold admission while doing model work. Its completion
	// notifies this worker; do not block delivery to other target Sessions.
	if !r.mu.TryLock() {
		return wake.DeliveryResult{}, nil
	}
	defer r.mu.Unlock()
	if err := s.Store.Get(ctx, "sessions", batch.SessionID, &record); err != nil {
		return wake.DeliveryResult{}, err
	}
	if err := persistence.CheckSessionRunnable(ctx, record, s.Store); err != nil {
		return wake.DeliveryResult{}, err
	}
	if s.WakeEndpoints != nil && !s.WakeEndpoints.BatchActive(batch) {
		return wake.DeliveryResult{Handled: true}, nil
	}
	session := r.core.Load()
	receipt, err := session.LookupInput(ctx, batch.ID)
	if errors.Is(err, core.ErrNotFound) {
		if len(batch.Events) == 0 && !batch.Flood {
			return wake.DeliveryResult{}, domain.Conflict("accepted external event payload is unavailable")
		}
		message := messages.ExternalEvents(batch, record.BindingID != "")
		if _, err = r.accept(ctx, core.Input{ID: batch.ID, Digest: batch.Digest, Contexts: []core.Message{message}}, core.InputContext); err != nil {
			return wake.DeliveryResult{}, err
		}
		receipt, err = session.LookupInput(ctx, batch.ID)
	}
	if err != nil {
		return wake.DeliveryResult{}, err
	}
	if receipt.Digest != batch.Digest {
		return wake.DeliveryResult{}, domain.Conflict("external event batch identity was used for different content")
	}
	result := wake.DeliveryResult{Accepted: true}
	// Standalone adapters without the endpoint controller retain Core's
	// explicit cancellation boundary. Production uses the durable generation
	// check above, which distinguishes Owner Stop from Server shutdown.
	if s.WakeEndpoints == nil {
		if last := session.State().LastRun; last != nil && last.Status == "cancelled" {
			stopped, err := IMInputStopped(ctx, session, s.CoreStore, batch.ID)
			if err != nil || stopped {
				result.Handled = stopped
				return result, err
			}
		}
	}
	if r.externalInspector == nil || r.externalBatchID != batch.ID {
		r.externalInspector, r.externalBatchID = NewIMInspector(session, s.CoreStore), batch.ID
	}
	result.Handled, err = r.externalInspector.handled(ctx, batch.ID, true)
	if err != nil || result.Handled {
		if result.Handled {
			r.externalInspector, r.externalBatchID = nil, ""
		}
		return result, err
	}
	snapshot, err := r.snapshot()
	if err != nil {
		return result, err
	}
	if snapshot.Running || snapshot.Maintenance || snapshot.Stopping {
		return result, nil
	}
	if pending, err := s.ModelFallbackPending(ctx, batch.SessionID); err != nil || pending {
		return result, err
	}
	// A busy Run can finish before reading its newly accepted passive batch.
	// One stable activation input handles that tail without queuing one Run per
	// event or rebuilding the event body on retries/recovery.
	id := batch.ID + ":wakeup"
	activation, err := session.LookupInput(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		_, err = r.accept(ctx, core.Input{ID: id, Digest: digest(struct{ BatchID string }{batch.ID}), Message: messages.ExternalTrigger(id, batch.ID)}, core.InputSteer)
		return result, err
	}
	if err != nil {
		return result, err
	}
	if activation.State == core.InputApplied {
		// An old driver can be consumed before a crash or maintenance without
		// the real generation ever adopting its external context. Permit one
		// stable recovery driver for that boundary, never one per worker call.
		id, err = r.externalRecoveryID(ctx, batch.ID, activation)
		if err != nil || id == "" {
			return result, err
		}
		activation, err = session.LookupInput(ctx, id)
		if errors.Is(err, core.ErrNotFound) {
			_, err = r.accept(ctx, core.Input{ID: id, Digest: digest(struct{ BatchID string }{batch.ID}), Message: messages.ExternalTrigger(id, batch.ID)}, core.InputSteer)
			return result, err
		}
		if err != nil {
			return result, err
		}
	}
	if activation.State != core.InputPending {
		return result, nil
	}
	// A preparation/budget failure can leave the stable activation queued,
	// without freezing a generation that adopted the external batch. Reusing
	// that pending input on every Run-end notification would create an endless
	// sequence of failed Runs. Keep it queued for explicit Continue or the
	// existing model-fallback worker instead; failure is not model coverage.
	if last := session.State().LastRun; last != nil && last.Status == "failed" {
		covered, err := IMInputStopped(ctx, session, s.CoreStore, id)
		if err != nil || covered {
			return result, err
		}
	}
	if s.WakeEndpoints == nil {
		if stopped, err := IMInputStopped(ctx, session, s.CoreStore, id); err != nil || stopped {
			result.Handled = stopped
			return result, err
		}
	}
	// Only an existing, unapplied activation can need a crash-recovery start.
	// Applied or stopped activations never drive another Run on repeated calls.
	err = r.continueLocked(ctx)
	if errors.Is(err, core.ErrBusy) || errors.Is(err, core.ErrNotContinuable) {
		err = nil
	}
	return result, err
}

func (r *runtime) externalRecoveryID(ctx context.Context, batchID string, activation core.InputReceipt) (string, error) {
	if activation.AcceptedAt.Before(r.service.wakeStartedAt) {
		return batchID + ":resume:" + r.service.RuntimeID, nil
	}
	state := r.core.Load().State()
	if state.Branch.CompactionID == "" || activation.EntryID == "" {
		return "", nil
	}
	checkpoint, err := r.service.CoreStore.Get(ctx, state.RolloutID, state.Branch.CompactionID)
	if err != nil {
		return "", err
	}
	driver, err := r.service.CoreStore.Get(ctx, state.RolloutID, activation.EntryID)
	if err != nil {
		return "", err
	}
	if checkpoint.Entry.Sequence <= driver.Entry.Sequence {
		return "", nil
	}
	return batchID + ":compact:" + digest(state.Branch.CompactionID)[:24], nil
}

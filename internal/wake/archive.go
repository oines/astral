package wake

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

// PauseSession is the durable half of Session archiving. Keep endpoint
// configuration and idempotency receipts, while discarding unhandled payloads.
// The caller owns the transaction; this function never takes service locks.
func PauseSession(ctx context.Context, tx *persistence.Tx, sessionID string) error {
	return PauseSessions(ctx, tx, []string{sessionID})
}

// PauseSessions scans endpoint configuration once for the delegated tree.
func PauseSessions(ctx context.Context, tx *persistence.Tx, sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	selected := make(map[string]bool, len(sessionIDs))
	for _, id := range sessionIDs {
		selected[id] = true
	}
	items, err := tx.List(ctx, Kind)
	if err != nil {
		return err
	}
	for _, item := range items {
		var endpoint domain.WakeEndpoint
		if err := json.Unmarshal(item.Data, &endpoint); err != nil {
			return err
		}
		if !selected[endpoint.SessionID] {
			continue
		}
		if endpoint.Enabled {
			endpoint.Enabled = false
			if err := tx.Save(ctx, Kind, &endpoint.Meta, &endpoint); err != nil {
				return err
			}
		}
		var state endpointState
		if err := tx.Maintenance(ctx, stateKind, endpoint.ID, &state); domain.IsMissing(err) {
			continue
		} else if err != nil {
			return err
		}
		if !state.BlockedUntil.IsZero() {
			state.BlockedUntil = time.Time{}
			if err := tx.SaveMaintenance(ctx, stateKind, endpoint.ID, "active", state); err != nil {
				return err
			}
		}
	}
	for id := range selected {
		if err := tx.DeleteMaintenance(ctx, flowKind, id); err != nil {
			return err
		}
	}
	return nil
}

// InvalidateSession follows the archive commit while Harness still owns
// admission. An old worker callback cannot adopt a future buffer generation.
func (s *Service) InvalidateSession(id string) {
	s.InvalidateSessions([]string{id})
}

// InvalidateSessions drops one sealed tree in one pass and emits one directory
// invalidation, rather than broadcasting a full configuration per child.
func (s *Service) InvalidateSessions(ids []string) {
	if len(ids) == 0 {
		return
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	s.mu.Lock()
	for id := range selected {
		s.discardFlowLocked(id)
	}
	for id, endpoint := range s.cache {
		if selected[endpoint.SessionID] {
			delete(s.cache, id)
		}
	}
	for id, state := range s.states {
		if selected[state.SessionID] {
			delete(s.states, id)
		}
	}
	s.mu.Unlock()
	s.configurationChanged()
}

func runnableTarget(ctx context.Context, store persistence.SessionAccessReader, agentID, id string) error {
	var session domain.Session
	if err := store.Get(ctx, "sessions", id, &session); err != nil {
		return err
	}
	if session.AgentID != agentID {
		return domain.Missing("Session", id)
	}
	return persistence.CheckSessionRunnable(ctx, session)
}

var errInactiveFlow = errors.New("external event buffer is no longer active")

// A Session archive transaction deletes the durable flow before invalidating
// its in-memory copy. Recheck inside each worker write so neither side of an
// in-flight callback can recreate a discarded buffer in that small interval.
func activeFlow(ctx context.Context, tx *persistence.Tx, expected *flow) error {
	if err := runnableTarget(ctx, tx, expected.AgentID, expected.SessionID); err != nil {
		var conflict *domain.Error
		if domain.IsMissing(err) || errors.As(err, &conflict) && conflict.Status == 409 {
			return errInactiveFlow
		}
		return err
	}
	var stored struct {
		Generation uint64 `json:"generation"`
	}
	if err := tx.Maintenance(ctx, flowKind, expected.SessionID, &stored); domain.IsMissing(err) {
		return errInactiveFlow
	} else if err != nil {
		return err
	}
	if stored.Generation != expected.Generation {
		return errInactiveFlow
	}
	return nil
}

func (s *Service) discardFlowLocked(id string) {
	delete(s.flows, id)
	delete(s.retry, id)
	delete(s.dirty, id)
}

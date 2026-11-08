package wake

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func revokedComputer(ctx context.Context, store persistence.SessionAccessReader, id string) error {
	var computer domain.Computer
	if err := store.Get(ctx, "computers", id, &computer); domain.IsMissing(err) {
		return nil // A retry still cleans residual endpoints and event buffers.
	} else if err != nil {
		return err
	}
	if !computer.Revoked {
		return domain.Conflict("revoke the Computer before deleting its event endpoints")
	}
	return nil
}

func computerEvents(events []BatchEvent, id string) bool {
	for _, event := range events {
		if event.ComputerID == id {
			return true
		}
	}
	return false
}

func (s *Service) computerDeletionState(ctx context.Context, computerID string) ([]domain.WakeEndpoint, map[string]*flow, error) {
	if err := revokedComputer(ctx, s.store, computerID); err != nil {
		return nil, nil, err
	}
	rows, err := s.store.List(ctx, Kind)
	if err != nil {
		return nil, nil, err
	}
	endpoints := []domain.WakeEndpoint{}
	for _, row := range rows {
		var endpoint domain.WakeEndpoint
		if err := json.Unmarshal(row.Data, &endpoint); err != nil {
			return nil, nil, err
		}
		if endpoint.ComputerID == computerID {
			endpoints = append(endpoints, endpoint)
		}
	}
	rowsPending, err := s.store.PendingMaintenance(ctx, flowKind)
	if err != nil {
		return nil, nil, err
	}
	flows := map[string]*flow{}
	for _, raw := range rowsPending {
		var f flow
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, nil, err
		}
		if computerEvents(f.Buffer, computerID) || f.Inflight != nil && computerEvents(f.Inflight.Events, computerID) {
			flows[f.SessionID] = &f
		}
	}
	return endpoints, flows, nil
}

// SnapshotComputerTargets identifies the admission locks needed by permanent
// Computer deletion. A revoked or missing source cannot add new valid targets.
// Flow provenance also covers an endpoint that was manually deleted earlier.
func (s *Service) SnapshotComputerTargets(ctx context.Context, computerID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	endpoints, flows, err := s.computerDeletionState(ctx, computerID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(endpoints)+len(flows))
	for _, endpoint := range endpoints {
		seen[endpoint.SessionID] = true
	}
	for id := range flows {
		seen[id] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func withoutComputer(events []BatchEvent, computerID string) []BatchEvent {
	remaining := make([]BatchEvent, 0, len(events))
	for _, event := range events {
		if event.ComputerID != computerID {
			remaining = append(remaining, event)
		}
	}
	return remaining
}

func eventBytes(events []BatchEvent) int {
	bytes := 0
	seen := map[string]bool{}
	for _, event := range events {
		bytes += len(event.Text)
		if !seen[event.EndpointID] {
			seen[event.EndpointID] = true
			bytes += len(event.Title) + len(event.Prompt) + len(event.Notes)
		}
	}
	return bytes
}

// WithComputerDeletion requires Harness to hold every SnapshotComputerTargets
// Session's admission and runtime-registration gates. It never acquires those
// gates itself. coreLookup must only query CoreStore's public receipt API; it
// must not open a runtime or wait for admission while this service holds mu.
// deleteTx removes the Computer in the same Host transaction as event cleanup.
func (s *Service) WithComputerDeletion(ctx context.Context, computerID string,
	coreLookup func(context.Context, string, string, string) (bool, error),
	deleteTx func(*persistence.Tx) error,
) error {
	s.mu.Lock()
	endpoints, existing, err := s.computerDeletionState(ctx, computerID)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	updated := make(map[string]*flow, len(existing))
	epoch := s.generation
	for id, original := range existing {
		next := *original
		next.Buffer = withoutComputer(original.Buffer, computerID)
		next.Bytes = eventBytes(next.Buffer)
		if len(next.Buffer) == 0 {
			next.Due, next.Flood = time.Time{}, false
		} else if len(next.Buffer) != len(original.Buffer) {
			next.Flood = len(next.Buffer) >= MaxBatchEvents || next.Bytes >= MaxBatchBytes
		}
		var accepted bool
		if original.Inflight != nil {
			if coreLookup == nil {
				s.mu.Unlock()
				return domain.Conflict("Core receipt lookup is required for an in-flight event batch")
			}
			accepted, err = coreLookup(ctx, id, original.Inflight.ID, original.Inflight.Digest)
			if err != nil {
				s.mu.Unlock()
				return err
			}
		}
		epoch = max(epoch, original.Generation) + 1
		next.Generation = epoch
		if original.Inflight != nil {
			batch := *original.Inflight
			batch.Generation = epoch // Host control token, never a new Core input.
			if accepted {
				// Core may have committed before the Host acknowledgement failed.
				// Keep its exact ID/digest; replaying remaining sources is unsafe.
				batch.Events, batch.Flood = nil, false
				next.Inflight = &batch
			} else {
				batch.Events = withoutComputer(batch.Events, computerID)
				if len(batch.Events) == 0 {
					next.Inflight = nil
				} else {
					batch.ID, batch.Digest = domain.ID("wake_batch"), ""
					batch.Flood = len(batch.Events) >= MaxBatchEvents || eventBytes(batch.Events) >= MaxBatchBytes
					batch.Digest = hash(batch)
					next.Inflight = &batch
				}
			}
		}
		if next.Inflight != nil || len(next.Buffer) > 0 {
			updated[id] = &next
		} else {
			updated[id] = nil
		}
	}
	err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := revokedComputer(ctx, tx, computerID); err != nil {
			return err
		}
		if deleteTx != nil {
			if err := deleteTx(tx); err != nil {
				return err
			}
		}
		if err := tx.PurgeComputerWakeEndpoints(ctx, computerID); err != nil {
			return err
		}
		for id, next := range updated {
			if next == nil {
				if err := tx.DeleteMaintenance(ctx, flowKind, id); err != nil {
					return err
				}
			} else if err := tx.SaveMaintenance(ctx, flowKind, id, "pending", next); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.generation = epoch
		for _, endpoint := range endpoints {
			delete(s.cache, endpoint.ID)
			delete(s.states, endpoint.ID)
		}
		for id, endpoint := range s.cache {
			if endpoint.ComputerID == computerID {
				delete(s.cache, id)
				delete(s.states, id)
			}
		}
		for id, next := range updated {
			s.discardFlowLocked(id)
			if next != nil {
				s.flows[id] = next
				s.dirty[id] = struct{}{}
			}
		}
	}
	s.mu.Unlock()
	if err == nil {
		s.configurationChanged()
		s.signal()
	}
	return err
}

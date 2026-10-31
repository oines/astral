package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/datamigrate"
)

type reclaimRecord struct {
	Reclaim
	Next               int    `json:"next"`
	ObservedDirtyToken string `json:"observed_dirty_token,omitempty"`
}

func (s *Service) RequestReclaim() {
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	s.reclaimGeneration++
	s.reclaim = Reclaim{State: "queued", RequestedAt: &now, UpdatedAt: &now}
	s.reclaimDirty = true
	s.reclaimStep = 0
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) reclaimWorker() {
	defer s.workers.Done()
	interval := s.config.reclaimInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-tick.C:
		}
		if err := s.observeReclaimDirty(s.ctx); err != nil {
			continue
		}
		if err := s.persistReclaim(); err != nil {
			continue
		}
		s.mu.Lock()
		state := s.reclaim
		generation := s.reclaimGeneration
		dirtyToken := s.reclaimObserved
		s.mu.Unlock()
		if state.State != "queued" && state.State != "waiting" {
			continue
		}
		if state.RequestedAt != nil && time.Since(*state.RequestedAt) < interval {
			continue
		}
		if s.config.Busy != nil && s.config.Busy() {
			s.reclaimState(generation, "waiting", "Waiting for active Bot work to finish.")
			continue
		}
		if s.config.Updating != nil && s.config.Updating(s.ctx) {
			s.reclaimState(generation, "waiting", "Waiting for the application update to finish.")
			continue
		}
		if _, err := os.Stat(filepath.Join(s.config.Directory, "migration-state.json")); err == nil {
			s.reclaimState(generation, "waiting", "Waiting for the data migration to finish.")
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			s.reclaimState(generation, "failed", err.Error())
			continue
		}
		// Pin the current directory before Host VACUUM owns its single SQLite
		// connection. Management polling can then show the running task without
		// queuing a Host metadata read behind the maintenance it is inspecting.
		if _, err := s.Snapshot(s.ctx, ""); err != nil {
			s.reclaimState(generation, "failed", err.Error())
			continue
		}
		s.reclaimState(generation, "running", "")
		// The service lifecycle cancels maintenance on shutdown. Large databases
		// are not forced to restart the same VACUUM by a size-blind deadline.
		err := s.runReclaim(s.ctx, generation)
		if s.ctx.Err() != nil {
			return
		}
		if err != nil {
			if errors.Is(err, core.ErrBusy) || strings.Contains(strings.ToLower(err.Error()), "locked") || strings.Contains(strings.ToLower(err.Error()), "busy") {
				s.reclaimState(generation, "waiting", err.Error())
			} else {
				s.reclaimState(generation, "failed", err.Error())
			}
		} else if err := s.config.Store.ClearStorageReclaimDirty(s.ctx, dirtyToken); err != nil {
			s.reclaimState(generation, "failed", err.Error())
		} else {
			s.reclaimState(generation, "complete", "")
			s.Refresh(false)
		}
	}
}

// A token is committed by the transaction finishing a logical deletion. Its
// discovery does not rely on the callback, so startup recovery and a crash
// before the worker saves its queue cannot lose the reclamation request.
func (s *Service) observeReclaimDirty(ctx context.Context) error {
	token, err := s.config.Store.StorageReclaimDirty(ctx)
	if err != nil {
		return err
	}
	if token == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == s.reclaimObserved && s.reclaim.State != "idle" && s.reclaim.State != "complete" {
		return nil
	}
	now := time.Now().UTC()
	s.reclaimObserved = token
	s.reclaimGeneration++
	s.reclaimStep = 0
	s.reclaimDirty = true
	s.reclaim = Reclaim{State: "queued", RequestedAt: &now, UpdatedAt: &now}
	return nil
}
func (s *Service) reclaimState(generation uint64, state, message string) {
	s.mu.Lock()
	if generation != s.reclaimGeneration {
		s.mu.Unlock()
		return
	}
	if s.reclaim.State == state && s.reclaim.Error == message {
		s.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	s.reclaim.State, s.reclaim.Error, s.reclaim.UpdatedAt = state, message, &now
	s.reclaimDirty = true
	s.mu.Unlock()
	_ = s.persistReclaim()
}
func (s *Service) persistReclaim() error {
	return s.persistReclaimContext(s.ctx)
}

func (s *Service) persistReclaimContext(ctx context.Context) error {
	s.reclaimPersistMu.Lock()
	defer s.reclaimPersistMu.Unlock()
	s.mu.Lock()
	state := reclaimRecord{Reclaim: s.reclaim, Next: s.reclaimStep, ObservedDirtyToken: s.reclaimObserved}
	generation := s.reclaimGeneration
	dirty := s.reclaimDirty
	s.mu.Unlock()
	if !dirty {
		return nil
	}
	if err := s.config.Store.SaveMaintenance(ctx, "storage_reclaim", "owner", state.State, state); err != nil {
		s.mu.Lock()
		if generation == s.reclaimGeneration {
			s.reclaim.Error = "Could not persist reclamation progress; retrying: " + err.Error()
		}
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	if generation == s.reclaimGeneration && state.State == s.reclaim.State && state.Error == s.reclaim.Error {
		s.reclaimDirty = false
	}
	s.mu.Unlock()
	return nil
}
func (s *Service) runReclaim(ctx context.Context, generation uint64) error {
	if err := datamigrate.ReclaimSpace(s.config.Directory); err != nil {
		return err
	}
	reclaimer, ok := s.config.Core.(interface{ Reclaim(context.Context) error })
	if !ok {
		return errors.New("this Core history store does not support online space reclamation")
	}
	for index, reclaim := range append([]func(context.Context) error{reclaimer.Reclaim}, s.config.Reclaimers...) {
		s.mu.Lock()
		next := s.reclaimStep
		current := s.reclaimGeneration
		s.mu.Unlock()
		if generation != current {
			return core.ErrBusy
		}
		if index < next {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := reclaim(ctx); err != nil {
			return err
		}
		s.mu.Lock()
		if generation == s.reclaimGeneration {
			s.reclaimStep = index + 1
			s.reclaimDirty = true
		}
		s.mu.Unlock()
		if err := s.persistReclaim(); err != nil {
			return err
		}
	}
	return nil
}

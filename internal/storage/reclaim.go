package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/oines/astral/internal/datamigrate"
)

func (s *Service) RequestReclaim() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	s.reclaimPending = true
	if s.reclaimDone == nil {
		s.reclaimDone = make(chan struct{})
	}
	now := time.Now().UTC()
	s.reclaim.RequestedAt = &now
	if s.reclaim.State != "running" {
		s.reclaim.State, s.reclaim.Error, s.reclaim.UpdatedAt = "queued", "", &now
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// WaitForReclaim joins the current batch, including requests that arrive while
// a vacuum is running. With no batch to join it returns immediately.
func (s *Service) WaitForReclaim(ctx context.Context) error {
	s.mu.Lock()
	done := s.reclaimDone
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return s.ctx.Err()
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
		s.mu.Lock()
		pending := s.reclaimPending && time.Since(*s.reclaim.RequestedAt) >= interval
		s.mu.Unlock()
		if !pending {
			continue
		}
		if s.config.Busy != nil && s.config.Busy() || s.config.Updating != nil && s.config.Updating(s.ctx) {
			s.setReclaim("waiting", "")
			continue
		}
		_, err := os.Stat(filepath.Join(s.config.Directory, "migration-state.json"))
		if err == nil {
			s.setReclaim("waiting", "")
			continue
		}
		s.mu.Lock()
		s.reclaimPending = false
		s.mu.Unlock()
		if errors.Is(err, os.ErrNotExist) {
			// Keep the last measured directory readable while SQLite owns its write connection.
			s.setReclaim("running", "")
			err = s.runReclaim(s.ctx)
		}
		s.mu.Lock()
		now := time.Now().UTC()
		state, message := "complete", ""
		switch {
		case s.reclaimPending:
			state = "queued"
		case err != nil:
			message = err.Error()
			state = "failed"
		}
		s.reclaim.State, s.reclaim.Error, s.reclaim.UpdatedAt = state, message, &now
		if !s.reclaimPending {
			close(s.reclaimDone)
			s.reclaimDone = nil
		}
		s.mu.Unlock()
	}
}

func (s *Service) setReclaim(state, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reclaim.State == state && s.reclaim.Error == message {
		return
	}
	now := time.Now().UTC()
	s.reclaim.State, s.reclaim.Error, s.reclaim.UpdatedAt = state, message, &now
}

func (s *Service) runReclaim(ctx context.Context) error {
	if err := datamigrate.ReclaimSpace(s.config.Directory); err != nil {
		return err
	}
	coreStore, ok := s.config.Core.(interface{ Reclaim(context.Context) error })
	if !ok {
		return errors.New("this Core history store does not support online space reclamation")
	}
	if err := coreStore.Reclaim(ctx); err != nil {
		return err
	}
	for _, reclaim := range s.config.Reclaimers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := reclaim(ctx); err != nil {
			return err
		}
	}
	return nil
}

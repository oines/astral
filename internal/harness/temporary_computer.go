package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func (s *Service) handleTemporaryComputerConnected(computer domain.Computer) {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	s.refreshSessionTreeEnvironment(ctx, computer.OwnerSessionID)
	s.notify(computer.OwnerSessionID)
	s.notify("")
}

func (s *Service) cleanupTemporaryComputerAsync(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = s.CleanupTemporaryComputer(ctx, id)
}

// DisconnectTemporaryComputer immediately revokes, disconnects, and removes a
// Session-scoped temporary Computer.
func (s *Service) DisconnectTemporaryComputer(ctx context.Context, id string) error {
	var computer domain.Computer
	if err := s.Store.Get(ctx, "computers", id, &computer); err != nil {
		if domain.IsMissing(err) {
			return nil
		}
		return err
	}
	if !computer.Temporary {
		return domain.Invalid("only temporary Computers can be disconnected and removed in one step")
	}
	return s.CleanupTemporaryComputer(ctx, id)
}

// CleanupTemporaryComputer revokes, deletes, clears active Session pointers to
// the temporary Computer (without substituting another Computer), refreshes
// passive Agent context, and notifies UI subscribers.
func (s *Service) CleanupTemporaryComputer(ctx context.Context, id string) error {
	var computer domain.Computer
	if err := s.Store.Get(ctx, "computers", id, &computer); err != nil {
		if domain.IsMissing(err) {
			return nil
		}
		return err
	}
	if !computer.Temporary {
		return domain.Conflict("Computer is not temporary")
	}
	ownerSessionID := computer.OwnerSessionID
	if !computer.Revoked {
		if err := s.Hub.Revoke(ctx, id); err != nil && !domain.IsMissing(err) {
			return err
		}
	}
	clearedSessions, err := s.clearTemporaryComputerSelections(ctx, ownerSessionID, id)
	if err != nil {
		return err
	}
	var current domain.Computer
	if err := s.Store.Get(ctx, "computers", id, &current); err == nil {
		if err := s.DeleteComputer(ctx, id, current.Revision); err != nil && !domain.IsMissing(err) {
			return err
		}
	} else if !domain.IsMissing(err) {
		return err
	}
	if ownerSessionID != "" {
		s.refreshSessionTreeEnvironment(ctx, ownerSessionID)
		s.notify(ownerSessionID)
	}
	for _, sid := range clearedSessions {
		if sid != ownerSessionID {
			s.notify(sid)
		}
	}
	s.notify("")
	return nil
}

func (s *Service) clearTemporaryComputerSelections(ctx context.Context, ownerSessionID, computerID string) ([]string, error) {
	var cleared []string
	err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
		items, err := tx.List(ctx, "sessions")
		if err != nil {
			return err
		}
		for _, item := range items {
			var session domain.Session
			if err := json.Unmarshal(item.Data, &session); err != nil {
				continue
			}
			if session.Deletion != nil || session.ComputerID != computerID {
				continue
			}
			if ownerSessionID != "" && domain.RootSessionID(session) != ownerSessionID {
				continue
			}
			session.ComputerID = ""
			session.CWD = ""
			if err := tx.Save(ctx, "sessions", &session.Meta, &session); err != nil {
				return err
			}
			cleared = append(cleared, session.ID)
		}
		return nil
	})
	return cleared, err
}

func (s *Service) refreshSessionTreeEnvironment(ctx context.Context, rootSessionID string) {
	if rootSessionID == "" {
		return
	}
	targets := []string{rootSessionID}
	if items, err := s.Store.List(ctx, "sessions"); err == nil {
		for _, item := range items {
			var session domain.Session
			if json.Unmarshal(item.Data, &session) == nil && session.Deletion == nil && session.SubagentParentID == rootSessionID {
				targets = append(targets, session.ID)
			}
		}
	}
	for _, sid := range targets {
		// Archive seals the tree and releases its runtimes under subagentMu.
		// Serialize opening with that boundary, then release it before discovery.
		if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
			return
		}
		r, err := s.openRunnable(ctx, sid)
		s.subagentMu.Unlock()
		if err != nil {
			continue
		}
		if err := lockSubagentContext(ctx, &r.mu); err != nil {
			return
		}
		snap, snapErr := r.snapshot()
		if snapErr == nil && !snap.Running && !snap.Maintenance && r.requireRunnable(ctx) == nil {
			_ = r.refreshEnvironment(ctx)
		}
		r.mu.Unlock()
	}
}

// CleanupSessionTemporaryComputers removes every temporary Computer owned by
// the given root Session.
func (s *Service) CleanupSessionTemporaryComputers(ctx context.Context, rootSessionID string) error {
	if rootSessionID == "" {
		return nil
	}
	return s.cleanupTemporaryComputers(ctx, func(c domain.Computer) bool { return c.OwnerSessionID == rootSessionID })
}

func (s *Service) cleanupTemporaryComputers(ctx context.Context, owned func(domain.Computer) bool) error {
	items, err := s.Store.List(ctx, "computers")
	if err != nil {
		return err
	}
	for _, item := range items {
		var c domain.Computer
		if err := json.Unmarshal(item.Data, &c); err != nil {
			return err
		}
		if c.Temporary && owned(c) {
			if err := s.CleanupTemporaryComputer(ctx, c.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// RecoverTemporaryComputers runs on Server startup to clean up expired or
// orphaned temporary Computers and arm grace timers for any still-valid ones.
func (s *Service) RecoverTemporaryComputers(ctx context.Context) {
	items, err := s.Store.List(ctx, "computers")
	if err != nil {
		return
	}
	now := time.Now()
	for _, item := range items {
		var c domain.Computer
		if json.Unmarshal(item.Data, &c) != nil || !c.Temporary {
			continue
		}
		if c.Revoked || c.ExpiresAt == nil || now.After(*c.ExpiresAt) {
			_ = s.CleanupTemporaryComputer(ctx, c.ID)
			continue
		}
		var owner domain.Session
		if err := s.Store.Get(ctx, "sessions", c.OwnerSessionID, &owner); err != nil || owner.Deletion != nil || owner.Archived {
			_ = s.CleanupTemporaryComputer(ctx, c.ID)
			continue
		}
		// Still within lifetime on restart: give a grace window to reconnect.
		s.Hub.ArmTemporaryTimers(c.ID, *c.ExpiresAt)
	}
}

func temporaryComputerNotice(name, id string, connected bool) string {
	if connected {
		return fmt.Sprintf("Temporary Computer %s (%s) connected for this Session and its subagents (no Astral API); call switch_computer if you need to work on it.", name, id)
	}
	return fmt.Sprintf("Temporary Computer %s (%s) disconnected and was removed.", name, id)
}

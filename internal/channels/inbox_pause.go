package channels

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const inboxPauseKind = "im_inbox_pause"
const inboxResumeKind = "im_inbox_resume"

type inboxResume struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Version   int64  `json:"version"`
}

type inboxBoundary struct {
	Model         inboxModelState
	ResumeVersion int64
}

type inboxModelState struct {
	Selection        core.ModelSelection `json:"selection"`
	ModelRevision    int64               `json:"model_revision"`
	ProviderID       string              `json:"provider_id"`
	ProviderRevision int64               `json:"provider_revision"`
}

// The failure belongs to one binding/Session, not to the platform poller.
// Queue identities remain in Core and platform observations remain archived.
type inboxPause struct {
	AgentID   string          `json:"agent_id"`
	SessionID string          `json:"session_id"`
	BindingID string          `json:"binding_id"`
	Model     inboxModelState `json:"model"`
	Error     string          `json:"error"`
	Failures  int             `json:"failures"`
	RetryAt   time.Time       `json:"retry_at,omitempty"`
}

func (s *Service) inboxModel(ctx context.Context, id string) (inboxModelState, error) {
	selection, err := s.Harness.ModelSelection(ctx, id)
	state := inboxModelState{Selection: selection}
	if err != nil {
		return state, err
	}
	var profile domain.ModelProfile
	if err = s.Store.Get(ctx, "models", selection.ID, &profile); domain.IsMissing(err) {
		return state, nil
	} else if err != nil {
		return state, err
	}
	state.ModelRevision, state.ProviderID = profile.Revision, profile.ProviderID
	var provider domain.Provider
	if err = s.Store.Get(ctx, "providers", profile.ProviderID, &provider); domain.IsMissing(err) {
		return state, nil
	} else if err != nil {
		return state, err
	}
	state.ProviderRevision = provider.Revision
	return state, nil
}

func (s *Service) pausedInbox(ctx context.Context, binding domain.ConversationBinding) (bool, error) {
	var pause inboxPause
	if err := s.Store.Maintenance(ctx, inboxPauseKind, binding.ID, &pause); domain.IsMissing(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	state, err := s.inboxModel(ctx, binding.SessionID)
	if err != nil {
		return false, err
	}
	a, _ := json.Marshal(state)
	b, _ := json.Marshal(pause.Model)
	if pause.SessionID == binding.SessionID && pause.AgentID == binding.AgentID && string(a) == string(b) {
		return pause.RetryAt.IsZero() || time.Now().Before(pause.RetryAt), nil
	}
	// A model/provider edit is an explicit recovery boundary. Ordinary queue
	// commits, new IM messages and process restarts do not change this key.
	return false, s.Store.DeleteMaintenance(ctx, inboxPauseKind, binding.ID)
}

func (s *Service) inboxBoundary(ctx context.Context, binding domain.ConversationBinding) (inboxBoundary, error) {
	state, err := s.inboxModel(ctx, binding.SessionID)
	if err != nil {
		return inboxBoundary{}, err
	}
	var resume inboxResume
	if err = s.Store.Maintenance(ctx, inboxResumeKind, binding.ID, &resume); err != nil && !domain.IsMissing(err) {
		return inboxBoundary{}, err
	}
	return inboxBoundary{Model: state, ResumeVersion: resume.Version}, nil
}

func (s *Service) pauseInboxFailure(ctx context.Context, binding domain.ConversationBinding, boundary inboxBoundary, cause error) error {
	var api *models.APIError
	permanent := errors.As(cause, &api) && (api.Kind == models.KindAuthentication || api.Kind == models.KindPermissionDenied || api.Kind == models.KindQuotaExhausted || api.StatusCode == 401 || api.StatusCode == 402 || api.StatusCode == 403)
	var classified *core.ModelError
	_ = errors.As(models.ModelError(cause), &classified)
	transient := classified != nil && classified.Transient || errors.Is(cause, context.DeadlineExceeded)
	if !permanent {
		available, _ := s.Harness.Models.Availability(ctx, boundary.Model.Selection.ID)
		if available && !transient && classified == nil {
			return nil
		}
		permanent = !available || !transient
	}
	pause := inboxPause{AgentID: binding.AgentID, SessionID: binding.SessionID, BindingID: binding.ID, Model: boundary.Model, Error: models.SafeError(cause).Error()}
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var resume inboxResume
		if err := tx.Maintenance(ctx, inboxResumeKind, binding.ID, &resume); err != nil && !domain.IsMissing(err) {
			return err
		}
		// A late error from work started before an explicit retry cannot pause
		// that new retry or accidentally bind an old failure to new settings.
		if resume.Version != boundary.ResumeVersion {
			return nil
		}
		var previous inboxPause
		if err := tx.Maintenance(ctx, inboxPauseKind, binding.ID, &previous); err != nil && !domain.IsMissing(err) {
			return err
		}
		pause.Failures = min(previous.Failures+1, 6)
		if !permanent {
			wait := min(30*time.Second*time.Duration(1<<(pause.Failures-1)), 10*time.Minute)
			if classified != nil && classified.RetryAfter > wait {
				wait = classified.RetryAfter
			}
			pause.RetryAt = time.Now().Add(wait)
		}
		return tx.SaveMaintenance(ctx, inboxPauseKind, binding.ID, "blocked", pause)
	})
}

func (s *Service) clearInboxFailure(ctx context.Context, binding domain.ConversationBinding, boundary inboxBoundary) error {
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var resume inboxResume
		if err := tx.Maintenance(ctx, inboxResumeKind, binding.ID, &resume); err != nil && !domain.IsMissing(err) {
			return err
		}
		if resume.Version != boundary.ResumeVersion {
			return nil
		}
		return tx.DeleteMaintenance(ctx, inboxPauseKind, binding.ID)
	})
}

// IMDeliveryError projects an existing failure into the Session's ordinary
// error surface. It performs only Host reads, including when View holds a lock.
func (s *Service) IMDeliveryError(ctx context.Context, id string) (string, error) {
	var session domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &session); err != nil {
		return "", err
	}
	if session.BindingID == "" {
		return "", nil
	}
	var pause inboxPause
	if err := s.Store.Maintenance(ctx, inboxPauseKind, session.BindingID, &pause); domain.IsMissing(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if pause.SessionID != id {
		return "", nil
	}
	return pause.Error, nil
}

// ResumeIMSession is the optional Harness callback for a successful explicit
// user Submit/Continue/Compact. It never re-enters Harness or runs a model.
func (s *Service) ResumeIMSession(ctx context.Context, id string) error {
	var session domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &session); domain.IsMissing(err) {
		return nil
	} else if err != nil {
		return err
	}
	if session.BindingID == "" {
		return nil
	}
	if err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var resume inboxResume
		if err := tx.Maintenance(ctx, inboxResumeKind, session.BindingID, &resume); err != nil && !domain.IsMissing(err) {
			return err
		}
		resume.AgentID, resume.SessionID = session.AgentID, session.ID
		resume.Version++
		if err := tx.SaveMaintenance(ctx, inboxResumeKind, session.BindingID, "complete", resume); err != nil {
			return err
		}
		return tx.DeleteMaintenance(ctx, inboxPauseKind, session.BindingID)
	}); err != nil {
		return err
	}
	s.mu.Lock()
	wake := s.inboxWake[session.BindingID]
	s.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
	return nil
}

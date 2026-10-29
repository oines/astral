package harness

import (
	"context"
	"errors"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/schedule"
)

func (s *Service) deliverSchedule(ctx context.Context, occurrence schedule.Occurrence) error {
	if occurrence.SessionMode == "new" {
		if occurrence.SessionID != occurrence.NewSessionID() {
			return domain.Invalid("scheduled Session identity does not match its occurrence")
		}
		if _, err := s.createSession(ctx, occurrence.AgentID, occurrence.Title, occurrence.SessionID, "schedule", true); err != nil {
			return err
		}
	}
	r, err := s.open(ctx, occurrence.SessionID)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var record domain.Session
	if err := s.Store.Get(ctx, "sessions", occurrence.SessionID, &record); err != nil {
		return err
	}
	if record.AgentID != occurrence.AgentID || record.Deletion != nil {
		return domain.Missing("Session", occurrence.SessionID)
	}
	id, hash := occurrence.InputID(), digest(occurrence)
	if _, exists, err := r.existingReceipt(ctx, id, hash); err != nil || exists {
		return err
	}
	location, err := s.Timezone.Location(ctx)
	if err != nil {
		return err
	}
	message := messages.Scheduled(id, occurrence.Prompt, messages.ScheduleMetadata{ID: occurrence.ScheduleID, Title: occurrence.Title, ScheduledAt: occurrence.ScheduledAt.In(location)}, time.Now().In(location), record.BindingID != "")
	_, err = r.accept(ctx, core.Input{ID: id, Digest: hash, Message: message}, core.InputSteer)
	return err
}

func (s *Service) scheduleAccepted(ctx context.Context, occurrence schedule.Occurrence) (bool, error) {
	receipt, err := s.CoreStore.LookupInput(ctx, occurrence.SessionID, occurrence.InputID())
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if receipt.Digest != digest(occurrence) {
		return false, domain.Conflict("scheduled input identity was used for different content")
	}
	return true, nil
}

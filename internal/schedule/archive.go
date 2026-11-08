package schedule

import (
	"context"
	"encoding/json"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

// PauseSession participates in the Session archive transaction without taking
// service locks. Source Sessions and previous results are not delivery targets.
func PauseSession(ctx context.Context, tx *persistence.Tx, sessionID string) error {
	return PauseSessions(ctx, tx, []string{sessionID})
}

// PauseSessions scans plan configuration once for the entire delegated tree.
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
		var plan record
		if err := json.Unmarshal(item.Data, &plan); err != nil {
			return err
		}
		if plan.SessionMode == "new" || !selected[plan.SessionID] || !plan.Enabled && plan.NextRunAt == nil && plan.Pending == nil {
			continue
		}
		if plan.Pending != nil {
			plan.LastStatus, plan.LastError = "missed", ""
		}
		plan.Enabled, plan.NextRunAt, plan.Pending = false, nil, nil
		if err := tx.Save(ctx, Kind, &plan.Meta, &plan); err != nil {
			return err
		}
	}
	return nil
}

func runnableTarget(ctx context.Context, store getter, mode, sessionID string) error {
	if mode == "new" {
		return nil
	}
	var session domain.Session
	if err := store.Get(ctx, "sessions", sessionID, &session); err != nil {
		return err
	}
	return persistence.CheckSessionRunnable(ctx, session)
}

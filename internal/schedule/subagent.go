package schedule

import (
	"context"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

var errSubagentSchedule = domain.Forbidden("Subagent work is controlled by its direct parent, not by schedules")

func requireScheduleController(ctx context.Context, store getter) error {
	scope, scoped := persistence.CurrentSessionScope(ctx)
	if !scoped {
		return nil
	}
	var caller domain.Session
	if err := store.Get(ctx, "sessions", scope.SessionID, &caller); err != nil {
		return err
	}
	if caller.AgentID != scope.AgentID || caller.Deletion != nil {
		return domain.Missing("Session", scope.SessionID)
	}
	if caller.SubagentParentID != "" {
		return errSubagentSchedule
	}
	return nil
}

func planTarget(ctx context.Context, store getter, plan domain.Schedule) error {
	if err := target(ctx, store, plan.AgentID, plan.SessionMode, plan.SessionID); err != nil {
		return err
	}
	if plan.SessionMode == "new" && plan.SourceSessionID != "" {
		var source domain.Session
		if err := store.Get(ctx, "sessions", plan.SourceSessionID, &source); err != nil {
			return err
		}
		if source.AgentID != plan.AgentID || source.Deletion != nil {
			return domain.Missing("Session", source.ID)
		}
		if source.SubagentParentID != "" {
			return errSubagentSchedule
		}
	}
	return nil
}

package persistence

import (
	"context"
	"errors"

	"github.com/oines/astral/internal/domain"
)

// CheckSessionRunnable is an execution gate, separate from history access.
// Archive transactions seal the whole delegated tree. The optional ancestry
// check also protects children of Sessions archived by older Host versions.
func CheckSessionRunnable(ctx context.Context, record domain.Session, readers ...SessionAccessReader) error {
	seen := map[string]bool{}
	for {
		if record.Deletion != nil {
			return domain.Missing("Session", record.ID)
		}
		if record.Archived {
			return &domain.Error{Status: 409, Code: "session_archived", Message: "会话已归档，请先恢复后再执行任务。"}
		}
		if record.SubagentParentID == "" || len(readers) == 0 || readers[0] == nil {
			return nil
		}
		if seen[record.ID] {
			return domain.Conflict("cyclic subagent relationship")
		}
		seen[record.ID] = true
		var parent domain.Session
		if err := readers[0].Get(ctx, "sessions", record.SubagentParentID, &parent); err != nil {
			return err
		}
		if parent.AgentID != record.AgentID {
			return domain.Missing("Session", record.ID)
		}
		record = parent
	}
}

func IsSessionArchived(err error) bool {
	var typed *domain.Error
	return errors.As(err, &typed) && typed.Code == "session_archived"
}

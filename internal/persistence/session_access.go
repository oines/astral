package persistence

import (
	"context"

	"github.com/oines/astral/internal/domain"
)

type sessionScopeKey struct{}

// SessionScope is set only from the Host-validated command binding. Owner API
// calls have no scope and retain access to every Session.
type SessionScope struct {
	AgentID   string
	SessionID string
}

func WithSessionScope(ctx context.Context, agentID, sessionID string) context.Context {
	return context.WithValue(ctx, sessionScopeKey{}, SessionScope{AgentID: agentID, SessionID: sessionID})
}

func CurrentSessionScope(ctx context.Context) (SessionScope, bool) {
	scope, ok := ctx.Value(sessionScopeKey{}).(SessionScope)
	return scope, ok
}

// SessionAccessReader supplies current delegation ancestry at read boundaries.
// A transaction must pass its Tx rather than the enclosing Store, which owns
// the same single database connection.
type SessionAccessReader interface {
	Get(context.Context, string, string, any) error
}

// CheckSessionAccess checks the current Host record at each resource boundary.
// Read boundaries may supply their Store or Tx to allow delegation ancestors
// to read descendants. Without a reader, the existing self/direct-parent scope
// remains unchanged; this does not grant ancestors child management rights.
// Old identifiers, Fork origins and paging snapshots are never grants.
func CheckSessionAccess(ctx context.Context, session domain.Session, readers ...SessionAccessReader) error {
	if session.Deletion != nil {
		return domain.Missing("Session", session.ID)
	}
	if scope, ok := CurrentSessionScope(ctx); ok {
		if scope.AgentID != session.AgentID {
			return domain.Missing("Session", session.ID)
		}
		// Privacy belongs to the target. A private caller can still read public
		// Sessions of its Bot, as well as itself.
		if !session.Private || scope.SessionID == session.ID {
			return nil
		}
		if scope.SessionID == "" || session.SubagentParentID == "" {
			return domain.Missing("Session", session.ID)
		}
		if len(readers) == 0 || readers[0] == nil {
			if scope.SessionID == session.SubagentParentID {
				return nil
			}
			return domain.Missing("Session", session.ID)
		}
		seen := map[string]bool{session.ID: true}
		ancestor := session
		allowed := false
		for ancestor.SubagentParentID != "" {
			id := ancestor.SubagentParentID
			if seen[id] {
				return domain.Missing("Session", session.ID)
			}
			seen[id] = true
			var parent domain.Session
			if err := readers[0].Get(ctx, "sessions", id, &parent); domain.IsMissing(err) {
				return domain.Missing("Session", session.ID)
			} else if err != nil {
				return err
			}
			if parent.AgentID != scope.AgentID || parent.Deletion != nil {
				return domain.Missing("Session", session.ID)
			}
			allowed = allowed || parent.ID == scope.SessionID
			ancestor = parent
		}
		if !allowed {
			return domain.Missing("Session", session.ID)
		}
	}
	return nil
}

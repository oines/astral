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

// CheckSessionAccess deliberately reads the current Host record at each
// resource boundary. Old identifiers and paging snapshots are not grants.
func CheckSessionAccess(ctx context.Context, session domain.Session) error {
	if session.Deletion != nil {
		return domain.Missing("Session", session.ID)
	}
	if scope, ok := CurrentSessionScope(ctx); ok && (scope.AgentID != session.AgentID || session.Private && scope.SessionID != session.ID) {
		return domain.Missing("Session", session.ID)
	}
	return nil
}

package modelbridge

import (
	"context"
	"github.com/oines/astral-core/models/integration"
)

type sessionContextKey struct{}

func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, id)
}
func sessionID(ctx context.Context) string {
	id, _ := ctx.Value(sessionContextKey{}).(string)
	return id
}

var sessionHeaders = integration.SessionHeaders

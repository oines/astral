//go:build !linux

package daemonservice

import "context"

func nativeRunner(_ string) commandRunner {
	return func(ctx context.Context, name string, args ...string) (commandResult, error) {
		return executeEnvironment(ctx, nil, name, args...)
	}
}

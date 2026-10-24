//go:build !windows

package platform

import (
	"context"
	"os"
	"time"
)

func watchParent(ctx context.Context, parentPID int, cancel context.CancelFunc) error {
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if os.Getppid() != parentPID {
					cancel()
					return
				}
			}
		}
	}()
	return nil
}

package platform

import (
	"context"
	"fmt"
	"io"
	"os"
)

// ManagedContext stops a desktop-owned Server when its parent dies or closes
// its private input pipe. The latter is an explicit graceful-shutdown request,
// including on Windows where child.kill cannot deliver POSIX signals.
func ManagedContext(ctx context.Context, parentPID int, shutdownInput io.Reader) (context.Context, context.CancelFunc, error) {
	managed, cancel := context.WithCancel(ctx)
	if parentPID <= 0 || parentPID != os.Getppid() {
		cancel()
		return nil, nil, fmt.Errorf("desktop parent process does not match")
	}
	if err := watchParent(managed, parentPID, cancel); err != nil {
		cancel()
		return nil, nil, err
	}
	if shutdownInput != nil {
		go func() {
			_, _ = io.Copy(io.Discard, shutdownInput)
			cancel()
		}()
	}
	return managed, cancel, nil
}

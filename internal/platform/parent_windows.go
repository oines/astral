package platform

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows"
)

func watchParent(ctx context.Context, parentPID int, cancel context.CancelFunc) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(parentPID))
	if err != nil {
		return fmt.Errorf("open desktop parent: %w", err)
	}
	go func() {
		defer windows.CloseHandle(handle)
		for ctx.Err() == nil {
			status, waitErr := windows.WaitForSingleObject(handle, 250)
			if waitErr != nil || status == windows.WAIT_OBJECT_0 {
				cancel()
				return
			}
		}
	}()
	return nil
}

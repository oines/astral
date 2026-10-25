package platform

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
)

func TestUpdateWaitsForNewBootAndExpectedVersion(t *testing.T) {
	var phase atomic.Int64
	_, credentials := secureStatusFixture(t, func() domain.ComputerStatus {
		status := domain.ComputerStatus{Online: true, BootID: "old"}
		status.DaemonVersion = "release"
		switch phase.Add(1) {
		case 1:
		case 2:
			status.BootID = "new"
			status.DaemonVersion = "wrong"
		default:
			status.BootID = "new"
		}
		return status
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := waitUpdatedComputer(ctx, credentials, "old", "release"); err != nil {
		t.Fatal(err)
	}
	if phase.Load() < 3 {
		t.Fatal("reported update before the new version connected")
	}
}

package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
)

func TestUpdateWaitsForNewBootAndExpectedVersion(t *testing.T) {
	var phase atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := waitUpdatedComputer(ctx, computers.Credentials{ServerURL: server.URL, ComputerID: "computer"}, "old", "release"); err != nil {
		t.Fatal(err)
	}
	if phase.Load() < 3 {
		t.Fatal("reported update before the new version connected")
	}
}

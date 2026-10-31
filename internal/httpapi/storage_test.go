package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/oines/astral/internal/storage"
	"github.com/oines/astral/internal/testkit"
)

func TestStorageManagementOwnerBoundaryAndCompatibleSummary(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("ok") })
	a, server := apiFixture(t, p)
	manager := storage.New(p.Context, storage.Config{Directory: p.Store.Directory, Store: p.Store, Core: p.CoreStore})
	a.Config.Storage = manager
	t.Cleanup(func() { manager.Close(context.Background()) })
	for _, path := range []string{"/storage", "/storage/manage", "/storage/cleanup"} {
		response := request(t, p.Context, server, http.MethodGet, "/api/v1"+path, nil, false)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous storage access accepted: %s %d", path, response.StatusCode)
		}
		response.Body.Close()
	}
	response := request(t, p.Context, server, http.MethodGet, "/api/v1/storage", nil, true)
	var legacy StorageInfo
	err := json.NewDecoder(response.Body).Decode(&legacy)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || legacy.Directory == "" {
		t.Fatalf("legacy storage summary changed: %+v %v", legacy, err)
	}
	response = request(t, p.Context, server, http.MethodGet, "/api/v1/storage/manage", nil, true)
	var snapshot storage.Snapshot
	err = json.NewDecoder(response.Body).Decode(&snapshot)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(snapshot.Agents) != 1 || snapshot.Agents[0].AgentID != p.Agent.ID || snapshot.Physical.DiskTotalBytes == 0 {
		t.Fatalf("invalid management response: %+v %v", snapshot, err)
	}
	response = request(t, p.Context, server, http.MethodPost, "/api/v1/storage/cleanup", bytes.NewBufferString(`{"operation_id":"bad","tokens":["invented-path"]}`), true)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("unreviewed file path accepted: %d", response.StatusCode)
	}
	response.Body.Close()
	response = request(t, p.Context, server, http.MethodPost, "/api/v1/storage/refresh", bytes.NewBufferString(`{"cancel":true}`), true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("cancel estimate failed: %d", response.StatusCode)
	}
	response.Body.Close()
}

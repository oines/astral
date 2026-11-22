package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
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
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("old per-item request reached cleanup-all: %d", response.StatusCode)
	}
	response.Body.Close()
	response = request(t, p.Context, server, http.MethodPost, "/api/v1/storage/cleanup/all", nil, true)
	var result storage.CleanupResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != http.StatusOK || result.Error != "" {
		t.Fatalf("cleanup all: %+v %v %d", result, err, response.StatusCode)
	}
	response.Body.Close()
	response = request(t, p.Context, server, http.MethodPost, "/api/v1/storage/refresh", nil, true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("refresh estimate failed: %d", response.StatusCode)
	}
	response.Body.Close()
}

func TestArchiveClearRequiresNewOwnerAction(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	account := domain.Connection{Meta: domain.Meta{ID: "account"}, AgentID: p.Agent.ID}
	chat := domain.Conversation{Meta: domain.Meta{ID: "chat"}, ConnectionID: account.ID}
	if err := p.Store.Save(p.Context, "connections", &account.Meta, &account); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
		t.Fatal(err)
	}
	event := domain.Observation{ID: "event", ExternalEventID: "external", ConnectionID: account.ID, ConversationID: chat.ID, Kind: "message.created", ReceivedAt: time.Now().UTC(), Text: "keep until explicitly cleared"}
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	const api = "/api/v1/storage/conversations/chat"
	response := request(t, p.Context, server, http.MethodPost, api+"/cleanup", bytes.NewBufferString(`{"before":"2000-01-01T00:00:00Z"}`), true)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("legacy cutoff triggered an action: %d", response.StatusCode)
	}
	response = request(t, p.Context, server, http.MethodDelete, api+"/archive", nil, false)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous clear: %d", response.StatusCode)
	}
	if _, err := p.Store.Observation(p.Context, event.ID); err != nil {
		t.Fatal("archive changed without an authorized clear", err)
	}
	response = request(t, p.Context, server, http.MethodDelete, api+"/archive", nil, true)
	defer response.Body.Close()
	var result ArchiveCleanupResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != 200 || result.RemovedCount != 1 {
		t.Fatalf("clear returned %+v, %d, %v", result, response.StatusCode, err)
	}
	if _, err := p.Store.Observation(p.Context, event.ID); !domain.IsMissing(err) {
		t.Fatal("clear returned before the archive was pruned", err)
	}
}

package httpapi

import (
	"net/http"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/storage"
)

func (a *API) storageRoutes() {
	ready := func() error {
		if a.Config.Storage == nil {
			return domain.Offline("storage management is unavailable on this Server")
		}
		return nil
	}
	route[httpcontract.Empty, storage.Snapshot](a, "GET", "/storage/manage", "manageStorage", "Storage", func(r *http.Request, _ httpcontract.Empty) (storage.Snapshot, error) {
		if err := ready(); err != nil {
			return storage.Snapshot{}, err
		}
		return a.Config.Storage.Snapshot(r.Context(), r.URL.Query().Get("agent_id"))
	}, httpcontract.Parameter{Name: "agent_id", Type: "string"})
	route[storage.RefreshRequest, storage.Snapshot](a, "POST", "/storage/refresh", "refreshStorage", "Storage", func(r *http.Request, input storage.RefreshRequest) (storage.Snapshot, error) {
		if err := ready(); err != nil {
			return storage.Snapshot{}, err
		}
		a.Config.Storage.Refresh(input.Cancel)
		return a.Config.Storage.Snapshot(r.Context(), "")
	})
	route[httpcontract.Empty, storage.Snapshot](a, "POST", "/storage/reclaim", "reclaimStorage", "Storage", func(r *http.Request, _ httpcontract.Empty) (storage.Snapshot, error) {
		if err := ready(); err != nil {
			return storage.Snapshot{}, err
		}
		a.Config.Storage.RequestReclaim()
		return a.Config.Storage.Snapshot(r.Context(), "")
	})
	route[httpcontract.Empty, storage.CleanupInventory](a, "GET", "/storage/cleanup", "storageCleanupInventory", "Storage", func(r *http.Request, _ httpcontract.Empty) (storage.CleanupInventory, error) {
		if err := ready(); err != nil {
			return storage.CleanupInventory{}, err
		}
		return a.Config.Storage.Inventory(r.Context()), nil
	})
	route[storage.CleanupRequest, storage.CleanupOperation](a, "POST", "/storage/cleanup", "cleanupStorage", "Storage", func(r *http.Request, input storage.CleanupRequest) (storage.CleanupOperation, error) {
		if err := ready(); err != nil {
			return storage.CleanupOperation{}, err
		}
		return a.Config.Storage.Cleanup(r.Context(), input)
	})
	route[httpcontract.Empty, storage.CleanupOperation](a, "GET", "/storage/cleanup/{id}", "storageCleanupOperation", "Storage", func(r *http.Request, _ httpcontract.Empty) (storage.CleanupOperation, error) {
		if err := ready(); err != nil {
			return storage.CleanupOperation{}, err
		}
		return a.Config.Storage.CleanupOperation(r.Context(), r.PathValue("id"))
	})
	route[httpcontract.Empty, storage.CleanupOperation](a, "POST", "/storage/cleanup/{id}/retry", "retryStorageCleanup", "Storage", func(r *http.Request, _ httpcontract.Empty) (storage.CleanupOperation, error) {
		if err := ready(); err != nil {
			return storage.CleanupOperation{}, err
		}
		return a.Config.Storage.RetryCleanup(r.Context(), r.PathValue("id"))
	})
}

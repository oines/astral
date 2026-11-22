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
		if r.URL.Query().Get("wait_for_reclaim") == "true" {
			if err := a.Config.Storage.WaitForReclaim(r.Context()); err != nil {
				return storage.Snapshot{}, err
			}
		}
		return a.Config.Storage.Snapshot(r.Context())
	}, httpcontract.Parameter{Name: "wait_for_reclaim", Type: "boolean", Description: "Wait for the current database reclamation batch to complete or fail before reading the snapshot."})
	route[httpcontract.Empty, storage.Snapshot](a, "POST", "/storage/refresh", "refreshStorage", "Storage", func(r *http.Request, _ httpcontract.Empty) (storage.Snapshot, error) {
		if err := ready(); err != nil {
			return storage.Snapshot{}, err
		}
		if err := a.Config.Storage.Refresh(r.Context()); err != nil {
			return storage.Snapshot{}, err
		}
		return a.Config.Storage.Snapshot(r.Context())
	})
	route[httpcontract.Empty, storage.Snapshot](a, "POST", "/storage/reclaim", "reclaimStorage", "Storage", func(r *http.Request, _ httpcontract.Empty) (storage.Snapshot, error) {
		if err := ready(); err != nil {
			return storage.Snapshot{}, err
		}
		a.Config.Storage.RequestReclaim()
		return a.Config.Storage.Snapshot(r.Context())
	})
	route[httpcontract.Empty, storage.CleanupResult](a, "POST", "/storage/cleanup/all", "cleanupStorage", "Storage", func(r *http.Request, _ httpcontract.Empty) (storage.CleanupResult, error) {
		if err := ready(); err != nil {
			return storage.CleanupResult{}, err
		}
		return a.Config.Storage.Cleanup(r.Context()), nil
	})
}

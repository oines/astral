package httpapi

import (
	"net/http"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
)

func (a *API) storageConversationRoutes() {
	for _, spec := range []struct{ path, kind, id string }{{"cleanup", "archive", "cleanConversationArchive"}, {"reset", "reset", "resetStorageConversation"}} {
		route[channels.StorageRequest, channels.StorageOperation](a, "POST", "/storage/conversations/{id}/"+spec.path, spec.id, "Storage", func(r *http.Request, input channels.StorageRequest) (channels.StorageOperation, error) {
			if a.Config.Channels == nil {
				return channels.StorageOperation{}, domain.Conflict("chat service unavailable")
			}
			return a.Config.Channels.StartStorageOperation(r.Context(), r.PathValue("id"), spec.kind, input)
		})
	}
	route[httpcontract.Empty, channels.StorageOperation](a, "GET", "/storage/operations/{id}", "storageOperation", "Storage", func(r *http.Request, _ httpcontract.Empty) (channels.StorageOperation, error) {
		if a.Config.Channels == nil {
			return channels.StorageOperation{}, domain.Conflict("chat service unavailable")
		}
		return a.Config.Channels.StorageOperation(r.Context(), r.PathValue("id"))
	})
	route[httpcontract.Empty, channels.StorageOperation](a, "POST", "/storage/operations/{id}/retry", "retryStorageOperation", "Storage", func(r *http.Request, _ httpcontract.Empty) (channels.StorageOperation, error) {
		if a.Config.Channels == nil {
			return channels.StorageOperation{}, domain.Conflict("chat service unavailable")
		}
		return a.Config.Channels.RetryStorageOperation(r.Context(), r.PathValue("id"))
	})
}

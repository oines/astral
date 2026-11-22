package httpapi

import (
	"net/http"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
)

type ArchiveCleanupResult struct {
	RemovedCount int `json:"removed_count"`
}
type ChatRetentionRequest struct {
	RetentionDays *int `json:"retention_days"`
}
type ResetChatResult struct {
	SessionID string `json:"session_id"`
}

func (a *API) storageConversationRoutes() {
	const path = "/storage/conversations/{id}"
	route[httpcontract.Empty, ArchiveCleanupResult](a, "DELETE", path+"/archive", "cleanConversationArchive", "Storage", func(r *http.Request, _ httpcontract.Empty) (ArchiveCleanupResult, error) {
		if a.Config.Channels == nil {
			return ArchiveCleanupResult{}, domain.Offline("chat service unavailable")
		}
		n, err := a.Config.Channels.ClearChatArchive(r.Context(), r.PathValue("id"))
		return ArchiveCleanupResult{RemovedCount: n}, err
	})
	route[ChatRetentionRequest, ArchiveCleanupResult](a, "PATCH", path+"/retention", "setConversationRetention", "Storage", func(r *http.Request, in ChatRetentionRequest) (ArchiveCleanupResult, error) {
		if a.Config.Channels == nil {
			return ArchiveCleanupResult{}, domain.Offline("chat service unavailable")
		}
		if in.RetentionDays == nil {
			return ArchiveCleanupResult{}, domain.Invalid("retention_days is required")
		}
		n, err := a.Config.Channels.SetChatRetention(r.Context(), r.PathValue("id"), *in.RetentionDays)
		return ArchiveCleanupResult{RemovedCount: n}, err
	})
	route[channels.ResetStorageRequest, ResetChatResult](a, "POST", path+"/reset", "resetStorageConversation", "Storage", func(r *http.Request, in channels.ResetStorageRequest) (ResetChatResult, error) {
		if a.Config.Channels == nil {
			return ResetChatResult{}, domain.Offline("chat service unavailable")
		}
		b, err := a.Config.Channels.ResetStorageChat(r.Context(), r.PathValue("id"), in)
		return ResetChatResult{SessionID: b.SessionID}, err
	})
}

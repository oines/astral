package gateway

import (
	"mime"
	"net/http"
	"strconv"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/httpcontract"
)

type TelegramStickerDownloadRequest struct {
	channels.TelegramStickerSource
	Path string `json:"path"`
}

func (s *Service) telegramStickerRoutes() {
	const contentPath = "/v1/accounts/{id}/telegram/stickers/content"
	s.Contract.Describe(httpcontract.Operation{
		Method: "GET", Path: contentPath, ID: "agentTelegramStickerContent", Tag: "Agent Gateway", CommandAuth: true,
		ContentType: "application/octet-stream",
		Query: []httpcontract.Parameter{
			{Name: "set_name", Type: "string"},
			{Name: "custom_emoji_id", Type: "string"},
			{Name: "file_id", Type: "string", Required: true},
		},
	})
	s.mux.HandleFunc("GET "+contentPath, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		download, err := s.Channels.TelegramStickerContent(r.Context(), binding(r), r.PathValue("id"), channels.TelegramStickerSource{
			SetName: query.Get("set_name"), CustomEmojiID: query.Get("custom_emoji_id"), FileID: query.Get("file_id"),
		})
		if err != nil {
			httpcontract.Failure(w, r, err)
			return
		}
		mediaType := download.MediaType
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": download.Name}))
		if download.Size > 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(download.Size, 10))
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusOK)
		w.(*responseWriter).stream = download.Body
	})
	register[TelegramStickerDownloadRequest, ReferenceResponse](s, "POST", "/v1/accounts/{id}/telegram/stickers/download", "agentDownloadTelegramSticker", func(r *http.Request, request TelegramStickerDownloadRequest) (ReferenceResponse, error) {
		ref, err := s.Channels.DownloadTelegramSticker(r.Context(), binding(r), r.PathValue("id"), request.TelegramStickerSource, request.Path)
		return ReferenceResponse{File: ref, URI: fileURI(ref)}, err
	})
}

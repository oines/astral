package gateway

import (
	"mime"
	"net/http"
	"strconv"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
)

type NapCatAvatarDownloadRequest struct {
	Path string `json:"path"`
}

func napcatAvatarQuery(r *http.Request) error {
	if r.URL.RawQuery != "" {
		return domain.Invalid("avatar resources do not accept query parameters")
	}
	return nil
}

func (s *Service) napcatAvatarRoutes() {
	for _, resource := range []struct {
		path, contentID, downloadID, description string
	}{
		{"/v1/conversations/{id}/avatar", "agentNapCatAvatarContent", "agentDownloadNapCatAvatar", "Read the selected authorized QQ NapCat chat's current avatar: the private peer's avatar or the group's avatar."},
		{"/v1/conversations/{id}/members/{user_id}/avatar", "agentNapCatMemberAvatarContent", "agentDownloadNapCatMemberAvatar", "Read a QQ user's current avatar through an authorized NapCat chat. The user must be the private peer or a verified current group member."},
	} {
		s.Contract.Describe(httpcontract.Operation{Method: "GET", Path: resource.path + "/content", ID: resource.contentID,
			Tag: "Agent Gateway", CommandAuth: true, ContentType: "application/octet-stream", Description: resource.description})
		s.mux.HandleFunc("GET "+resource.path+"/content", func(w http.ResponseWriter, r *http.Request) {
			if err := napcatAvatarQuery(r); err != nil {
				httpcontract.Failure(w, r, err)
				return
			}
			download, err := s.Channels.NapCatAvatarContent(r.Context(), binding(r), r.PathValue("id"), r.PathValue("user_id"))
			if err != nil {
				httpcontract.Failure(w, r, err)
				return
			}
			w.Header().Set("Content-Type", download.MediaType)
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": download.Name}))
			w.Header().Set("Content-Length", strconv.FormatInt(download.Size, 10))
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusOK)
			w.(*responseWriter).stream = download.Body
		})
		register[NapCatAvatarDownloadRequest, ReferenceResponse](s, "POST", resource.path+"/download", resource.downloadID, func(r *http.Request, request NapCatAvatarDownloadRequest) (ReferenceResponse, error) {
			if err := napcatAvatarQuery(r); err != nil {
				return ReferenceResponse{}, err
			}
			ref, err := s.Channels.DownloadNapCatAvatar(r.Context(), binding(r), r.PathValue("id"), r.PathValue("user_id"), request.Path)
			return ReferenceResponse{File: ref, URI: fileURI(ref)}, err
		})
	}
}

package gateway

import (
	"net/http"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
)

func (s *Service) napcatAvatarRoutes() {
	for _, resource := range []struct {
		path, contentID, description string
	}{
		{"/v1/conversations/{id}/avatar", "agentNapCatAvatarContent", "Read the selected authorized QQ NapCat chat's current avatar: the private peer's avatar or the group's avatar."},
		{"/v1/conversations/{id}/members/{user_id}/avatar", "agentNapCatMemberAvatarContent", "Read a QQ user's current avatar through an authorized NapCat chat. The user must be the private peer or a verified current group member."},
	} {
		s.describeContent(httpcontract.Operation{Method: "GET", Path: resource.path + "/content", ID: resource.contentID,
			Tag: "Agent Gateway", CommandAuth: true, ContentType: "application/octet-stream", Description: resource.description})
		s.mux.HandleFunc("GET "+resource.path+"/content", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" {
				httpcontract.Failure(w, r, domain.Invalid("avatar resources do not accept query parameters"))
				return
			}
			download, err := s.Channels.NapCatAvatarContent(r.Context(), binding(r), r.PathValue("id"), r.PathValue("user_id"))
			if err != nil {
				httpcontract.Failure(w, r, err)
				return
			}
			serveContent(w, r, download)
		})
	}
}

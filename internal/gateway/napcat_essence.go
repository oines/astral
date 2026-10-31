package gateway

import (
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
)

func napcatEssenceSource(r *http.Request, media bool) (channels.NapCatEssenceSource, error) {
	for name, values := range r.URL.Query() {
		if (name != "msg_seq" && name != "msg_random" && (!media || name != "media_digest")) || len(values) != 1 || values[0] == "" {
			return channels.NapCatEssenceSource{}, domain.Invalid("unsupported or repeated essence resource query parameter")
		}
	}
	if media && r.URL.Query().Get("media_digest") == "" {
		return channels.NapCatEssenceSource{}, domain.Invalid("follow the selected part's media_digest content or download link")
	}
	return channels.NapCatEssenceSource{MessageID: r.PathValue("message_id"), MsgSeq: r.URL.Query().Get("msg_seq"), MsgRandom: r.URL.Query().Get("msg_random"), MediaDigest: r.URL.Query().Get("media_digest")}, nil
}

func napcatEssenceViews(conversationID string, source channels.NapCatEssenceSource, parts []domain.MessagePart) []MessagePartView {
	views := messagePartViews(conversationID, source.MessageID, parts)
	query := url.Values{"msg_seq": {source.MsgSeq}, "msg_random": {source.MsgRandom}}.Encode()
	var resources func([]MessagePartView, []domain.MessagePart)
	resources = func(views []MessagePartView, originals []domain.MessagePart) {
		for i := range views {
			view := &views[i]
			base := "/v1/conversations/" + url.PathEscape(conversationID) + "/essence/" + url.PathEscape(source.MessageID) + "/parts/" + url.PathEscape(view.Ref)
			view.ReadURL, view.RawURL = base+"?"+query, ""
			if view.Attachment != nil {
				mediaQuery := query + "&media_digest=" + channels.NapCatEssenceMediaDigest(*originals[i].Attachment)
				view.ContentURL, view.DownloadURL = base+"/content?"+mediaQuery, base+"/download?"+mediaQuery
			}
			if view.Forward != nil {
				for j := range view.Forward.Messages {
					resources(view.Forward.Messages[j].Parts, originals[i].Forward.Messages[j].Parts)
				}
			}
		}
	}
	resources(views, parts)
	return views
}

func (s *Service) napcatEssenceRoutes() {
	const root = "/v1/conversations/{id}/essence/{message_id}/parts"
	const selected = root + "/{ref}"
	query := []httpcontract.Parameter{
		{Name: "msg_seq", Type: "string", Description: "Original message sequence from the essence list; supply together with msg_random to survive synthetic/native ID changes."},
		{Name: "msg_random", Type: "string", Description: "Original message random value from the essence list; supply together with msg_seq."},
	}
	register[httpcontract.Empty, MessagePartsResponse](s, "GET", root, "agentNapCatEssenceParts", func(r *http.Request, _ httpcontract.Empty) (MessagePartsResponse, error) {
		source, err := napcatEssenceSource(r, false)
		if err != nil {
			return MessagePartsResponse{}, err
		}
		resource, err := s.Channels.NapCatEssenceParts(r.Context(), binding(r), r.PathValue("id"), source)
		return MessagePartsResponse{Parts: napcatEssenceViews(r.PathValue("id"), resource.Source, resource.Parts)}, err
	}, query...)
	register[httpcontract.Empty, MessagePartResponse](s, "GET", selected, "agentNapCatEssencePart", func(r *http.Request, _ httpcontract.Empty) (MessagePartResponse, error) {
		source, err := napcatEssenceSource(r, false)
		if err != nil {
			return MessagePartResponse{}, err
		}
		source, part, err := s.Channels.NapCatEssencePart(r.Context(), binding(r), r.PathValue("id"), source, r.PathValue("ref"))
		if err != nil {
			return MessagePartResponse{}, err
		}
		return MessagePartResponse{Part: napcatEssenceViews(r.PathValue("id"), source, []domain.MessagePart{part})[0]}, nil
	}, query...)
	mediaQuery := append(append([]httpcontract.Parameter{}, query...), httpcontract.Parameter{Name: "media_digest", Type: "string", Required: true, Description: "Opaque media locator fingerprint from the returned part link; not a file checksum. Refresh parts if it conflicts."})
	s.Contract.Describe(httpcontract.Operation{Method: "GET", Path: selected + "/content", ID: "agentNapCatEssenceContent", Tag: "Agent Gateway", CommandAuth: true, ContentType: "application/octet-stream", Query: mediaQuery})
	s.mux.HandleFunc("GET "+selected+"/content", func(w http.ResponseWriter, r *http.Request) {
		source, err := napcatEssenceSource(r, true)
		if err != nil {
			httpcontract.Failure(w, r, err)
			return
		}
		download, err := s.Channels.NapCatEssenceContent(r.Context(), binding(r), r.PathValue("id"), source, r.PathValue("ref"))
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
	register[MessagePartDownloadRequest, ReferenceResponse](s, "POST", selected+"/download", "agentDownloadNapCatEssencePart", func(r *http.Request, request MessagePartDownloadRequest) (ReferenceResponse, error) {
		source, err := napcatEssenceSource(r, true)
		if err != nil {
			return ReferenceResponse{}, err
		}
		ref, err := s.Channels.DownloadNapCatEssencePart(r.Context(), binding(r), r.PathValue("id"), source, r.PathValue("ref"), request.Path)
		return ReferenceResponse{File: ref, URI: fileURI(ref)}, err
	}, mediaQuery...)
}

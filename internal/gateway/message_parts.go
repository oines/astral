package gateway

import (
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
)

// These views deliberately exclude adapter handles, inline wire payloads and
// credential-bearing media URLs. Every resource link retains its parent chat.
type MessagePartView struct {
	Ref         string                 `json:"ref"`
	Type        string                 `json:"type" enum:"text,card,attachment,forward,unsupported"`
	Text        string                 `json:"text,omitempty"`
	Card        *MessageCardView       `json:"card,omitempty"`
	Attachment  *MessageAttachmentView `json:"attachment,omitempty"`
	Forward     *MessageForwardView    `json:"forward,omitempty"`
	ReadURL     string                 `json:"read_url"`
	RawURL      string                 `json:"raw_url,omitempty"`
	ContentURL  string                 `json:"content_url,omitempty"`
	DownloadURL string                 `json:"download_url,omitempty"`
}

type MessageCardView struct {
	Kind        string `json:"kind"`
	Source      string `json:"source,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
}

type MessageAttachmentView struct {
	Ref       string                  `json:"ref"`
	Kind      string                  `json:"kind"`
	Name      string                  `json:"name"`
	MediaType string                  `json:"media_type,omitempty"`
	Size      int64                   `json:"size,omitempty"`
	Sticker   *domain.StickerMetadata `json:"sticker,omitempty"`
}

type MessageForwardView struct {
	Status   string                 `json:"status" enum:"inline,unexpanded,truncated,unavailable"`
	Total    int                    `json:"total,omitempty"`
	Messages []ForwardedMessageView `json:"messages,omitempty"`
}

type ForwardedMessageView struct {
	Sender domain.Sender     `json:"sender"`
	SentAt *time.Time        `json:"sent_at,omitempty"`
	Parts  []MessagePartView `json:"parts"`
}

type MessagePartsResponse struct {
	Parts []MessagePartView `json:"parts"`
}

type MessagePartResponse struct {
	Part       MessagePartView `json:"part"`
	Offset     int             `json:"offset"`
	NextOffset *int            `json:"next_offset,omitempty"`
}

type MessagePartDownloadRequest struct {
	Path string `json:"path"`
}

// MessageObservationView preserves existing message metadata while projecting
// quoted trees through the same safe views as the dedicated part resources.
type MessageObservationView struct {
	domain.Observation
	Parts      []MessagePartView `json:"parts,omitempty"`
	ReplyParts []MessagePartView `json:"reply_parts,omitempty"`
}

type StoredMessageObservationView struct {
	Sequence    int64                  `json:"sequence"`
	Observation MessageObservationView `json:"observation"`
}

func MessageObservation(observation domain.Observation) MessageObservationView {
	parts := messagePartViews(observation.ConversationID, observation.MessageID, observation.Parts)
	replyParts := messagePartViews(observation.ConversationID, observation.ReplyID, observation.ReplyParts)
	observation.Parts, observation.ReplyParts = nil, nil
	return MessageObservationView{Observation: observation, Parts: parts, ReplyParts: replyParts}
}

func messagePartURL(conversationID, messageID, ref string) string {
	return "/v1/conversations/" + url.PathEscape(conversationID) + "/messages/" + url.PathEscape(messageID) + "/parts/" + url.PathEscape(ref)
}

func messagePartViews(conversationID, messageID string, parts []domain.MessagePart) []MessagePartView {
	views := make([]MessagePartView, 0, len(parts))
	for _, part := range parts {
		view := MessagePartView{Ref: part.Ref, Type: part.Type, Text: part.Text, ReadURL: messagePartURL(conversationID, messageID, part.Ref)}
		if card := part.Card; card != nil {
			view.Card = &MessageCardView{Kind: card.Kind, Source: card.Source, Title: card.Title, Description: card.Description, URL: card.URL}
			if len(card.Raw) != 0 {
				view.RawURL = view.ReadURL + "/raw"
			}
		}
		if attachment := part.Attachment; attachment != nil {
			view.Attachment = &MessageAttachmentView{Ref: attachment.Ref, Kind: attachment.Kind, Name: attachment.Name, MediaType: attachment.MediaType, Size: attachment.Size, Sticker: attachment.Sticker}
			view.ContentURL, view.DownloadURL = view.ReadURL+"/content", view.ReadURL+"/download"
		}
		if forward := part.Forward; forward != nil {
			view.Forward = &MessageForwardView{Status: forward.Status, Total: forward.Total}
			for _, message := range forward.Messages {
				view.Forward.Messages = append(view.Forward.Messages, ForwardedMessageView{Sender: message.Sender, SentAt: message.SentAt, Parts: messagePartViews(conversationID, messageID, message.Parts)})
			}
		}
		views = append(views, view)
	}
	return views
}

func partPageQuery(r *http.Request) (int, int, error) {
	for name, values := range r.URL.Query() {
		if (name != "offset" && name != "limit") || len(values) != 1 {
			return 0, 0, domain.Invalid("message parts accept only one offset and limit parameter")
		}
	}
	values := []int{0, 20}
	for i, name := range []string{"offset", "limit"} {
		if r.URL.Query().Has(name) {
			value, err := strconv.Atoi(r.URL.Query().Get(name))
			if err != nil || value < 0 || name == "limit" && (value < 1 || value > 50) {
				return 0, 0, domain.Invalid("offset must be nonnegative and limit must be between 1 and 50")
			}
			values[i] = value
		}
	}
	return values[0], values[1], nil
}

func (s *Service) messagePartRoutes() {
	const root = "/v1/conversations/{id}/messages/{message_id}/parts"
	const selected = root + "/{ref}"
	register[httpcontract.Empty, MessagePartsResponse](s, "GET", root, "agentMessageParts", func(r *http.Request, _ httpcontract.Empty) (MessagePartsResponse, error) {
		parts, err := s.Channels.MessageParts(r.Context(), binding(r), r.PathValue("id"), r.PathValue("message_id"))
		return MessagePartsResponse{Parts: messagePartViews(r.PathValue("id"), r.PathValue("message_id"), parts)}, err
	})
	register[httpcontract.Empty, MessagePartResponse](s, "GET", selected, "agentMessagePart", func(r *http.Request, _ httpcontract.Empty) (MessagePartResponse, error) {
		offset, limit, err := partPageQuery(r)
		if err != nil {
			return MessagePartResponse{}, err
		}
		page, err := s.Channels.MessagePart(r.Context(), binding(r), r.PathValue("id"), r.PathValue("message_id"), r.PathValue("ref"), offset, limit)
		if err != nil {
			return MessagePartResponse{}, err
		}
		return MessagePartResponse{Part: messagePartViews(r.PathValue("id"), r.PathValue("message_id"), []domain.MessagePart{page.Part})[0], Offset: page.Offset, NextOffset: page.NextOffset}, nil
	}, httpcontract.Parameter{Name: "offset", Type: "integer", Description: "Absolute zero-based forwarded message offset; default 0."},
		httpcontract.Parameter{Name: "limit", Type: "integer", Description: "Forwarded messages per page, 1–50; default 20."})
	register[httpcontract.Empty, json.RawMessage](s, "GET", selected+"/raw", "agentMessagePartRaw", func(r *http.Request, _ httpcontract.Empty) (json.RawMessage, error) {
		return s.Channels.MessagePartRaw(r.Context(), binding(r), r.PathValue("id"), r.PathValue("message_id"), r.PathValue("ref"))
	})
	const content = selected + "/content"
	s.Contract.Describe(httpcontract.Operation{Method: "GET", Path: content, ID: "agentMessagePartContent", Tag: "Agent Gateway", CommandAuth: true, ContentType: "application/octet-stream"})
	s.mux.HandleFunc("GET "+content, func(w http.ResponseWriter, r *http.Request) {
		download, err := s.Channels.MessagePartContent(r.Context(), binding(r), r.PathValue("id"), r.PathValue("message_id"), r.PathValue("ref"))
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
	register[MessagePartDownloadRequest, ReferenceResponse](s, "POST", selected+"/download", "agentDownloadMessagePart", func(r *http.Request, request MessagePartDownloadRequest) (ReferenceResponse, error) {
		ref, err := s.Channels.DownloadMessagePart(r.Context(), binding(r), r.PathValue("id"), r.PathValue("message_id"), r.PathValue("ref"), request.Path)
		return ReferenceResponse{File: ref, URI: fileURI(ref)}, err
	})
}

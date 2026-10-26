package channels

import (
	"context"
	"github.com/oines/astral/internal/domain"
)

func (s *Service) downloadSource(ctx context.Context, agentID, sessionID string, attachment domain.Attachment, connection domain.Connection) (Download, error) {
	if attachment.FileURI != "" {
		file, _, body, err := s.Harness.Files.Read(ctx, agentID, sessionID, attachment.FileURI, 0, nil)
		return Download{Body: body, Name: file.DisplayName, Size: file.Size, MediaType: file.MediaType}, err
	}
	adapter, err := s.adapter(ctx, connection)
	if err != nil {
		return Download{}, err
	}
	return adapter.Download(ctx, attachment)
}

// AttachmentContent streams the original source. Neither the Server nor the
// receiving Computer needs a cached copy to serve this URL.
func (s *Service) AttachmentContent(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID, ref string) (Download, string, error) {
	_, _, connection, err := s.target(ctx, b.AgentID, b.SessionID, conversationID)
	if err != nil {
		return Download{}, "", err
	}
	attachment, err := s.Store.MessageAttachment(ctx, conversationID, messageID, ref)
	if err != nil {
		return Download{}, "", err
	}
	download, err := s.downloadSource(ctx, b.AgentID, b.SessionID, attachment, connection)
	mediaType := attachment.MediaType
	if download.MediaType != "" {
		mediaType = download.MediaType
	}
	return download, mediaType, err
}

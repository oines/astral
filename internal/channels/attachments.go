package channels

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/oines/astral/internal/domain"
)

// An attachment prerequisite failure is local authorization, persistence or
// credential access, rather than optional remote preview acquisition.
type attachmentPrerequisiteError struct{ cause error }

func (e *attachmentPrerequisiteError) Error() string { return e.cause.Error() }
func (e *attachmentPrerequisiteError) Unwrap() error { return e.cause }

func (s *Service) downloadSource(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID string, attachment domain.Attachment, connection domain.Connection) (Download, error) {
	if attachment.FileURI != "" {
		if strings.Contains(attachment.FileURI, "?") {
			if err := s.verifySentFile(ctx, b.AgentID, conversationID, messageID, attachment); err != nil {
				return Download{}, err
			}
		}
		info, body, err := s.Harness.Files.OpenSource(ctx, b, "", attachment.FileURI)
		return Download{Body: body, Name: info.Name, Size: info.Size, MediaType: info.MediaType}, err
	}
	adapter, err := s.adapter(ctx, connection)
	if err != nil {
		return Download{}, err
	}
	return adapter.Download(ctx, attachment)
}

// The path in an IM event is not itself authority to read a Computer. Check
// the local, accepted send for this exact attachment before opening that path.
func (s *Service) verifySentFile(ctx context.Context, agentID, conversationID, messageID string, attachment domain.Attachment) error {
	prefix, key, _ := strings.Cut(attachment.Ref, ":")
	id, number, _ := strings.Cut(key, ":")
	index, err := strconv.Atoi(number)
	if prefix != "outbox" || id == "" || err != nil || index < 0 {
		return domain.Forbidden("attachment has no outbound source")
	}
	var sent domain.Outbox
	if err := s.Store.Get(ctx, "outbox", id, &sent); err != nil {
		return err
	}
	if sent.AgentID != agentID || sent.ConversationID != conversationID || sent.Status != "accepted" || !slices.Contains(sent.PlatformMessageIDs, messageID) {
		return domain.Forbidden("attachment belongs to another outbound message")
	}
	if index >= len(sent.Parts) || sent.Parts[index].File == nil || sent.Parts[index].File.URI != attachment.FileURI {
		return domain.Forbidden("attachment does not match the sent file")
	}
	return nil
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
	download, err := s.downloadSource(ctx, b, conversationID, messageID, attachment, connection)
	mediaType := attachment.MediaType
	if download.MediaType != "" {
		mediaType = download.MediaType
	}
	return download, mediaType, err
}

package channels

import (
	"context"
	"errors"
	"fmt"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
)

const forwardPreviewTimeout = 6 * time.Second
const forwardPreviewImages = 4

// forwardObservationImages reuses ordinary IM image preparation. Only images
// in the rendered forward excerpt are read, in memory, and converted to native
// low-resolution content. Files and videos retain their download URLs without
// being fetched. The quoted source message remains the attachment's identity.
func (s *Service) forwardObservationImages(parent, previewContext context.Context, b domain.ConversationBinding, conversation domain.Conversation, event domain.Observation, connection domain.Connection) ([]core.Content, error) {
	var content []core.Content
	seen := map[string]bool{}
	count := 0
	for _, source := range []struct {
		messageID string
		parts     []domain.MessagePart
	}{{event.MessageID, event.Parts}, {event.ReplyID, event.ReplyParts}} {
		preview := messages.RichPartsPreview(source.parts, conversation.ID, source.messageID, event.SentAt.Location())
		for _, selected := range preview.Images {
			key := selected.MessageID + ":" + selected.Ref
			if seen[key] {
				continue
			}
			seen[key] = true
			if parent.Err() != nil {
				return nil, parent.Err()
			}
			if count >= forwardPreviewImages || previewContext.Err() != nil {
				content = append(content, core.Text(fmt.Sprintf("[Forwarded image preview omitted: message-id=%q ref=%q; the preparation budget was reached. Original metadata and download-url are retained.]", selected.MessageID, selected.Ref))...)
				continue
			}
			count++
			imageEvent := event
			imageEvent.MessageID = selected.MessageID
			image, err := s.observationImage(previewContext, b, conversation, imageEvent, connection, selected.Attachment)
			if err != nil {
				if parent.Err() != nil {
					return nil, parent.Err()
				}
				var optional *observationPreviewError
				var prerequisite *attachmentPrerequisiteError
				if previewContext.Err() == nil && (errors.As(err, &prerequisite) || !errors.As(err, &optional)) {
					return nil, err
				}
				content = append(content, core.Text(fmt.Sprintf("[Forwarded image preview unavailable: message-id=%q ref=%q; the image could not be downloaded or decoded. Original metadata and download-url are retained.]", selected.MessageID, selected.Ref))...)
				continue
			}
			for i := range image {
				if image[i].Type == "image" {
					image[i].URL = messages.MessagePartContentPath(conversation.ID, selected.MessageID, selected.Ref)
				}
			}
			// Bind the pixels to their quotation even when a current message and
			// its reply happen to use the same local part path.
			content = append(content, core.Text(fmt.Sprintf("[Forwarded image: message-id=%q ref=%q]", selected.MessageID, selected.Ref))...)
			content = append(content, image...)
		}
	}
	return content, nil
}

package channels

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
)

func (s *Service) inbox(ctx context.Context, b domain.ConversationBinding) {
	position := cursor{Meta: domain.Meta{ID: b.ID}, Sequence: b.StartSequence}
	if err := s.Store.Get(ctx, "inbox_cursors", b.ID, &position); err != nil && !domain.IsMissing(err) {
		return
	}
	if err := s.Harness.ResumeContext(ctx, b.SessionID); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "IM passive recovery failed", "session_id", b.SessionID, "error", err)
	}
	results := make(chan probeResult, 1)
	probing := false
	for ctx.Err() == nil {
		events, err := s.Store.Observations(ctx, b.ConversationID, position.Sequence, 100)
		if err != nil {
			if !delay(ctx, time.Second) {
				return
			}
			continue
		}
		for _, event := range events {
			if event.Observation.Trigger != "" && !event.Observation.Self {
				if err = s.coverProbe(ctx, b, event.Sequence); err != nil {
					break
				}
			}
			err = s.deliver(ctx, b, event.Observation)
			if err != nil {
				break
			}
			position.Sequence = event.Sequence
			if err = s.Store.Save(ctx, "inbox_cursors", &position.Meta, &position); err != nil {
				break
			}
		}
		select {
		case result := <-results:
			probing = false
			if probeErr := s.finishProbe(ctx, b, result); probeErr != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "IM Probe completion failed", "conversation_id", b.ConversationID, "error", probeErr)
			}
		default:
		}
		if !probing && err == nil {
			window, probeErr := s.pendingProbe(ctx, b, position.Sequence, time.Now())
			if probeErr == nil && window.Through != 0 {
				if _, lookupErr := s.Harness.Receipt(ctx, b.SessionID, probeInputID(b.ID, window.Through)); lookupErr == nil {
					_ = s.finishProbe(ctx, b, probeResult{through: window.Through, participate: true})
				} else {
					view, viewErr := s.Harness.InspectContext(ctx, b.SessionID)
					if viewErr == nil {
						s.mu.Lock()
						if s.deletingAgents[b.AgentID] {
							s.mu.Unlock()
							return
						}
						group := s.agentWorkersLocked(b.AgentID)
						group.workers.Add(1)
						s.workers.Add(1)
						s.mu.Unlock()
						probing = true
						go func() {
							defer s.workers.Done()
							defer group.workers.Done()
							request, cancel := context.WithTimeout(ctx, time.Minute)
							defer cancel()
							participate, requestErr := s.shouldParticipate(request, b, view)
							results <- probeResult{through: window.Through, participate: participate, err: requestErr}
						}()
					} else if !errors.Is(viewErr, core.ErrIncompleteBatch) {
						_ = s.finishProbe(ctx, b, probeResult{through: window.Through, err: viewErr})
					}
				}
			}
		}
		interval := 200 * time.Millisecond
		if err != nil {
			interval = time.Second
		}
		if !delay(ctx, interval) {
			return
		}
	}
}
func (s *Service) deliver(ctx context.Context, b domain.ConversationBinding, event domain.Observation) (err error) {
	defer func() {
		if err != nil && ctx.Err() == nil {
			_ = s.Store.SaveDelivery(ctx, persistence.Delivery{EventID: event.ID, SessionID: b.SessionID, State: "blocked", Error: err.Error()})
		}
	}()
	deliveryID := event.ID
	if event.Trigger != "" && !event.Self {
		deliveryID += ":trigger"
	}
	// Reconcile the durable Core receipt before trying to fetch any source
	// attachment. A retry after deletion/offline must not reinject a committed input.
	if receipt, receiptErr := s.Harness.Receipt(ctx, b.SessionID, deliveryID); receiptErr == nil && receipt.InputID != "" {
		return s.Store.SaveDelivery(ctx, persistence.Delivery{EventID: event.ID, SessionID: b.SessionID, State: "routed"})
	} else if receiptErr != nil && !domain.IsMissing(receiptErr) {
		return receiptErr
	}
	var connection domain.Connection
	if err = s.Store.Get(ctx, "connections", event.ConnectionID, &connection); err != nil {
		return err
	}
	if !connection.Enabled {
		return domain.Offline("channel Connection is disabled")
	}
	var conversation domain.Conversation
	if err = s.Store.Get(ctx, "conversations", event.ConversationID, &conversation); err != nil {
		return err
	}
	observation := messages.Observation(event, connection, conversation, nil, "")
	for _, attachment := range event.Attachments {
		sticker, thumbnail := stickerImage(event.Attachments, attachment)
		if thumbnail && sticker.Format == "static" {
			// The static original is already an image; retain its thumbnail for
			// downloads without sending the model a second copy of the sticker.
			continue
		}
		if attachment.Sticker != nil && attachment.Sticker.Format != "static" && attachment.Sticker.ThumbnailRef == "" {
			observation.Content = append(observation.Content, core.Text("[Sticker preview unavailable: Telegram supplied no thumbnail. The animated or video original remains available through its download-url.]")...)
		}
		if attachment.Kind != "image" && !strings.HasPrefix(attachment.MediaType, "image/") {
			continue
		}
		content, err := s.observationImage(ctx, b, conversation, event, connection, attachment)
		if err != nil {
			if sticker != nil && ctx.Err() == nil {
				// A thumbnail is optional, and static sticker pixels can also be
				// unavailable. Preserve the received sticker and its reusable ID.
				observation.Content = append(observation.Content, core.Text("[Sticker preview unavailable: the image could not be downloaded or decoded. Original sticker metadata and download-url are retained.]")...)
				continue
			}
			return err
		}
		observation.Content = append(observation.Content, content...)
	}
	var trigger *core.Message
	if event.Trigger != "" && !event.Self {
		value := messages.Trigger(event)
		trigger = &value
	}
	_, err = s.Harness.Deliver(ctx, b.SessionID, observation, trigger, b.ID)
	if err != nil {
		return err
	}
	return s.Store.SaveDelivery(ctx, persistence.Delivery{EventID: event.ID, SessionID: b.SessionID, State: "routed"})
}

func stickerImage(attachments []domain.Attachment, image domain.Attachment) (*domain.StickerMetadata, bool) {
	if image.Sticker != nil {
		return image.Sticker, false
	}
	for _, original := range attachments {
		if original.Sticker != nil && original.Sticker.ThumbnailRef != "" && original.Sticker.ThumbnailRef == image.Ref {
			return original.Sticker, true
		}
	}
	return nil, false
}

func (s *Service) observationImage(ctx context.Context, b domain.ConversationBinding, conversation domain.Conversation, event domain.Observation, connection domain.Connection, attachment domain.Attachment) ([]core.Content, error) {
	download, err := s.downloadSource(ctx, b.AgentID, b.SessionID, attachment, connection)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(download.Body, (20<<20)+1))
	closeErr := download.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if len(data) > 20<<20 {
		return core.Text("[Image preview omitted: original exceeds the 20 MiB inline image preparation budget. The attachment remains available through its download-url.]"), nil
	}
	processed, err := core.ProcessImage(ctx, data, core.ImageOptions{})
	if err != nil {
		return nil, err
	}
	if processed.IsError {
		return nil, domain.Invalid(processed.Content[0].Text)
	}
	image := processed.Content[1]
	image.URL = messages.AttachmentDownloadPath(conversation.ID, event.MessageID, attachment.Ref)
	return []core.Content{image}, nil
}

// DeliveryView combines the fixed channel destination with Core's current
// input receipt. Application state is never copied back into the Host database.
type DeliveryView struct {
	persistence.Delivery
	InputID string `json:"input_id,omitempty"`
	EntryID string `json:"entry_id,omitempty"`
}

func (s *Service) Delivery(ctx context.Context, eventID string) (DeliveryView, error) {
	route, err := s.Store.Delivery(ctx, eventID)
	delivery := DeliveryView{Delivery: route}
	if err != nil {
		return delivery, err
	}
	if delivery.State != "routed" {
		return delivery, nil
	}
	event, err := s.Store.Observation(ctx, eventID)
	if err != nil {
		return delivery, err
	}
	key := eventID
	if event.Trigger != "" && !event.Self {
		key += ":trigger"
	}
	receipt, err := s.Harness.Receipt(ctx, delivery.SessionID, key)
	if err != nil {
		return delivery, err
	}
	delivery.State, delivery.InputID, delivery.EntryID, delivery.Error = receipt.State, receipt.InputID, receipt.EntryID, receipt.Error
	return delivery, nil
}

// DownloadAttachment writes a platform attachment directly to the authenticated
// caller's original Computer and explicit path, even if the Session switched.
func (s *Service) DownloadAttachment(ctx context.Context, b domain.ExecutionBinding, conversationID, eventID, attachmentRef, destination string) (domain.FileRef, error) {
	if destination == "" {
		return domain.FileRef{}, domain.Invalid("an explicit destination path is required")
	}
	_, conversation, connection, err := s.target(ctx, b.AgentID, b.SessionID, conversationID)
	if err != nil {
		return domain.FileRef{}, err
	}
	event, err := s.Store.Observation(ctx, eventID)
	if err != nil {
		return domain.FileRef{}, err
	}
	if event.ConversationID != conversation.ID {
		return domain.FileRef{}, domain.Forbidden("attachment is outside the selected Conversation")
	}
	var attachment domain.Attachment
	for _, candidate := range event.Attachments {
		if candidate.Ref == attachmentRef {
			attachment = candidate
			break
		}
	}
	if attachment.Ref == "" {
		return domain.FileRef{}, domain.Missing("attachment", attachmentRef)
	}
	download, err := s.downloadSource(ctx, b.AgentID, b.SessionID, attachment, connection)
	if err != nil {
		return domain.FileRef{}, err
	}
	defer download.Body.Close()
	_, body, err := s.Harness.Hub.Call(ctx, "file.write", computers.Call{Binding: b, Path: destination}, download.Body)
	if err != nil {
		return domain.FileRef{}, err
	}
	_, copyErr := io.Copy(io.Discard, body)
	err = errors.Join(copyErr, body.Close())
	if err != nil {
		return domain.FileRef{}, err
	}
	return s.Harness.Files.Reference(ctx, b, destination, false)
}

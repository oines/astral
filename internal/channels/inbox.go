package channels

import (
	"context"
	"errors"
	"io"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
)

type attachmentCopy struct {
	domain.Meta
	URI string `json:"uri"`
}

func (s *Service) inbox(ctx context.Context, b domain.ConversationBinding) {
	position := cursor{Meta: domain.Meta{ID: b.ID}, Sequence: b.StartSequence}
	if err := s.Store.Get(ctx, "inbox_cursors", b.ID, &position); err != nil && !domain.IsMissing(err) {
		return
	}
	for ctx.Err() == nil {
		events, err := s.Store.Observations(ctx, b.ConversationID, position.Sequence, 100)
		if err != nil {
			if !delay(ctx, time.Second) {
				return
			}
			continue
		}
		for _, event := range events {
			err = s.deliver(ctx, b, event.Observation)
			if err != nil {
				break
			}
			position.Sequence = event.Sequence
			if err = s.Store.Save(ctx, "inbox_cursors", &position.Meta, &position); err != nil {
				break
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
	if event.Self {
		// The IM archive keeps the platform echo. Core already owns the original
		// Assistant message, so the echo never becomes another model input.
		return s.Store.SaveDelivery(ctx, persistence.Delivery{EventID: event.ID, SessionID: b.SessionID, State: "self_echo"})
	}
	defer func() {
		if err != nil && ctx.Err() == nil {
			_ = s.Store.SaveDelivery(ctx, persistence.Delivery{EventID: event.ID, SessionID: b.SessionID, State: "blocked", Error: err.Error()})
		}
	}()
	deliveryID := event.ID
	if event.Trigger != "" {
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
	files := make([]domain.FileRef, 0, len(event.Attachments))
	for _, attachment := range event.Attachments {
		file, copyErr := s.copyAttachment(ctx, b, event, attachment, connection)
		if copyErr != nil {
			return copyErr
		}
		files = append(files, file)
	}
	observation := messages.Observation(event, connection, conversation, files, "")
	var trigger *core.Message
	if event.Trigger != "" {
		value := messages.Trigger(event)
		trigger = &value
	}
	s.gate.Lock()
	defer s.gate.Unlock()
	current, _, _, err := s.target(ctx, b.AgentID, b.SessionID, event.ConversationID)
	if err != nil {
		return err
	}
	if current.ID != b.ID {
		return domain.Conflict("the original inbound binding is no longer active")
	}
	_, err = s.Harness.Deliver(ctx, b.SessionID, observation, trigger)
	if err != nil {
		return err
	}
	return s.Store.SaveDelivery(ctx, persistence.Delivery{EventID: event.ID, SessionID: b.SessionID, State: "routed"})
}
func (s *Service) copyAttachment(ctx context.Context, b domain.ConversationBinding, event domain.Observation, a domain.Attachment, c domain.Connection) (domain.FileRef, error) {
	key := stableID("attachment", b.AgentID, event.ID, a.Ref)
	var copy attachmentCopy
	if err := s.Store.Get(ctx, "inbox_files", key, &copy); err == nil {
		return s.Harness.Files.Get(ctx, b.AgentID, copy.URI)
	} else if !domain.IsMissing(err) {
		return domain.FileRef{}, err
	}
	var session domain.Session
	if err := s.Store.Get(ctx, "sessions", b.SessionID, &session); err != nil {
		return domain.FileRef{}, err
	}
	binding, err := s.Harness.Hub.Bind(ctx, b.AgentID, b.SessionID, session.ComputerID, session.CWD)
	if err != nil {
		return domain.FileRef{}, err
	}
	adapter, err := s.adapter(ctx, c)
	if err != nil {
		return domain.FileRef{}, err
	}
	download, err := adapter.Download(ctx, a)
	if err != nil {
		return domain.FileRef{}, err
	}
	defer download.Body.Close()
	ref, err := s.Harness.Files.Upload(ctx, binding, a.Name, download.Body)
	if err != nil {
		return ref, err
	}
	copy = attachmentCopy{Meta: domain.Meta{ID: key}, URI: filestream.URL(ref)}
	err = s.Store.Save(ctx, "inbox_files", &copy.Meta, &copy)
	return ref, err
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
	if event.Trigger != "" {
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
	if attachment.ExternalID == "" {
		return domain.FileRef{}, domain.Missing("attachment", attachmentRef)
	}
	adapter, err := s.adapter(ctx, connection)
	if err != nil {
		return domain.FileRef{}, err
	}
	download, err := adapter.Download(ctx, attachment)
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

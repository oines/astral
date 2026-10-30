package channels

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/persistence"
)

type SendRequest struct {
	domain.MessageOptions
	IntentID         string                   `json:"intent_id"`
	ConversationID   string                   `json:"conversation_id,omitempty"`
	Text             string                   `json:"text,omitempty"`
	Parts            []domain.OutboundPart    `json:"parts,omitempty"`
	ReplyTo          string                   `json:"reply_to,omitempty"`
	FileURI          string                   `json:"file_uri,omitempty"`
	Path             string                   `json:"path,omitempty"`
	SourceComputerID string                   `json:"source_computer_id,omitempty"`
	AssistantEntryID string                   `json:"-"`
	BindingID        string                   `json:"-"`
	SourceBinding    *domain.ExecutionBinding `json:"-"`
	autoMediaTypes   bool
}

func (s *Service) SendMessage(ctx context.Context, agentID, sessionID, executionID string, target harness.MessageTarget, args harness.MessageArgs) ([]domain.Outbox, error) {
	request := SendRequest{MessageOptions: args.MessageOptions, IntentID: executionID, ConversationID: target.ConversationID, BindingID: target.BindingID, Text: args.Text, Parts: args.Parts, ReplyTo: args.ReplyTo, SourceBinding: target.Source}
	if target.Source != nil {
		request.SourceComputerID = target.Source.ComputerID
	}
	if len(args.Attachments) > 0 {
		if len(args.Parts) > 0 {
			return nil, domain.Invalid("parts cannot be combined with text or attachments")
		}
		if args.Text != "" {
			request.Parts = append(request.Parts, domain.OutboundPart{Type: "text", Text: args.Text})
		}
		request.Text = ""
		request.autoMediaTypes = true
		for _, attachment := range args.Attachments {
			part := domain.OutboundPart{Type: "file"}
			if strings.HasPrefix(attachment, "astral-file://") {
				part.FileURI = attachment
			} else {
				part.Path = attachment
			}
			request.Parts = append(request.Parts, part)
		}
	}
	return s.Send(ctx, agentID, sessionID, request)
}

// Send persists the intent, then returns the platform outcome to the caller.
// Enqueue remains available for internal scheduling and idempotent recovery.
func (s *Service) Send(ctx context.Context, agentID, sessionID string, request SendRequest) ([]domain.Outbox, error) {
	receipts, err := s.Enqueue(ctx, agentID, sessionID, request)
	if err != nil {
		return receipts, err
	}
	return s.waitReceipts(ctx, receipts)
}

// A rejection reaches the model or HTTP caller in the same result; no
// automatic retry or silent conversion to another format occurs here.
func (s *Service) waitReceipts(ctx context.Context, receipts []domain.Outbox) ([]domain.Outbox, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		complete := true
		for i := range receipts {
			if err := s.Store.Get(ctx, "outbox", receipts[i].ID, &receipts[i]); err != nil {
				return receipts, err
			}
			record := receipts[i]
			switch record.Status {
			case "accepted":
			case "failed", "blocked", "unknown":
				return receipts, domain.Invalid("send " + record.ID + " is " + record.Status + ": " + record.Error)
			default:
				complete = false
			}
		}
		if complete {
			return receipts, nil
		}
		select {
		case <-ctx.Done():
			return receipts, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) group(ctx context.Context, id string) ([]domain.Outbox, error) {
	all, err := list[domain.Outbox](ctx, s.Store, "outbox")
	if err != nil {
		return nil, err
	}
	result := []domain.Outbox{}
	for _, record := range all {
		if record.GroupID == id {
			result = append(result, record)
		}
	}
	slices.SortFunc(result, func(a, b domain.Outbox) int { return a.Fragment - b.Fragment })
	return result, nil
}
func (s *Service) existing(ctx context.Context, key, hash string) ([]domain.Outbox, error) {
	id, err := s.Store.Intent(ctx, key)
	if err != nil {
		return nil, err
	}
	var first domain.Outbox
	if err = s.Store.Get(ctx, "outbox", id, &first); err != nil {
		return nil, err
	}
	if first.RequestDigest != hash {
		return nil, domain.Conflict("send intent was already used for different content")
	}
	return s.group(ctx, key)
}
func (s *Service) Enqueue(ctx context.Context, agentID, sessionID string, request SendRequest) ([]domain.Outbox, error) {
	ctx = persistence.WithSessionScope(ctx, agentID, sessionID)
	if request.IntentID == "" || len(request.IntentID) > 200 {
		return nil, domain.Invalid("a stable send intent is required")
	}
	if strings.TrimSpace(request.Text) == "" && len(request.Parts) == 0 && request.MarkdownTemplate == nil && request.FileURI == "" && request.Path == "" {
		return nil, domain.Invalid("text or attachment is required")
	}
	key := stableID("send", agentID, sessionID, request.IntentID)
	digestBody := string(encoded(request))
	if request.autoMediaTypes {
		digestBody += "\x00infer-media-types"
	}
	hash := stableID("digest", digestBody)
	if existing, err := s.existing(ctx, key, hash); err == nil {
		if len(existing) > 0 {
			if _, _, _, err := s.target(ctx, agentID, sessionID, existing[0].ConversationID); err != nil {
				return nil, err
			}
		}
		return existing, nil
	} else if !domain.IsMissing(err) {
		return nil, err
	}
	var session domain.Session
	if err := s.Store.Get(ctx, "sessions", sessionID, &session); err != nil {
		return nil, err
	}
	if session.AgentID != agentID {
		return nil, domain.Forbidden("Session is outside the current Agent view")
	}
	binding, conversation, connection, err := s.target(ctx, agentID, sessionID, request.ConversationID)
	if err != nil {
		return nil, err
	}
	if request.BindingID != "" && request.BindingID != binding.ID {
		return nil, domain.Conflict("the original outbound binding is no longer active")
	}
	adapter, err := s.adapter(ctx, connection)
	if err != nil {
		return nil, err
	}
	if adapter.Capabilities().Adapter == "qq.napcat" && conversation.Kind == "private" {
		for _, part := range request.Parts {
			if part.Type == "mention" {
				return nil, domain.Invalid("NapCat native mentions are only supported in group chats")
			}
		}
	}
	if request.ReplyTo != "" && !adapter.Capabilities().ReplyReferences {
		return nil, domain.Invalid("this platform does not support outbound reply references")
	}
	if request.ReplyTo != "" && connection.Platform == "telegram" {
		if err := s.telegramMessagePrivacy(ctx, agentID, connection, conversation, request.ReplyTo); err != nil {
			return nil, err
		}
	}
	// OneBot reply IDs are global to the account. Require a known message in
	// this authorized chat before its preview can be exposed to recipients.
	if request.ReplyTo != "" && connection.Adapter == "qq.napcat" {
		if _, err := s.Store.Message(ctx, conversation.ID, request.ReplyTo); err != nil {
			return nil, domain.Forbidden("reply message is not archived in the selected chat")
		}
	}
	parts, err := s.freezeOutboundParts(ctx, agentID, sessionID, request, adapter.Capabilities())
	if err != nil {
		return nil, err
	}
	fragments, err := prepareOutboundParts(adapter, parts, request.MessageOptions)
	if err != nil {
		return nil, err
	}
	s.gate.Lock()
	defer s.gate.Unlock()
	// Source snapshotting can take time; recheck the destination before commit.
	current, _, fresh, err := s.target(ctx, agentID, sessionID, request.ConversationID)
	if err != nil {
		return nil, err
	}
	if current.ID != binding.ID || fresh.AccountID != connection.AccountID {
		return nil, domain.Conflict("the destination changed while preparing this send")
	}
	out := make([]domain.Outbox, 0, len(fragments))
	err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.RequireActiveAgent(ctx, agentID); err != nil {
			return err
		}
		firstID := domain.ID("outbox")
		existing, claimErr := tx.ClaimIntent(ctx, key, firstID)
		if claimErr != nil {
			return claimErr
		}
		if existing != firstID {
			var prior domain.Outbox
			if claimErr = tx.Get(ctx, "outbox", existing, &prior); claimErr != nil {
				return claimErr
			}
			if prior.RequestDigest != hash {
				return domain.Conflict("send intent was already used for different content")
			}
			return nil
		}
		for index, fragment := range fragments {
			id := firstID
			if index > 0 {
				id = domain.ID("outbox")
			}
			record := domain.Outbox{Meta: domain.Meta{ID: id}, GroupID: key, IntentKey: fmt.Sprintf("%s:%d", key, index), RequestDigest: hash,
				MessageOptions: fragment.MessageOptions,
				AgentID:        agentID, SessionID: sessionID, BindingID: binding.ID, ConversationID: conversation.ID, ConnectionID: connection.ID, ConnectionAccountID: connection.AccountID,
				ExternalID: conversation.ExternalID, TopicID: conversation.TopicID, AssistantEntryID: request.AssistantEntryID, Fragment: index, Text: fragment.Text, ReplyTo: request.ReplyTo, FileRefID: fragment.FileRefID, Parts: fragment.Parts, Status: "pending"}
			if claimErr = tx.Save(ctx, "outbox", &record.Meta, &record); claimErr != nil {
				return claimErr
			}
			out = append(out, record)
		}
		return nil
	})
	if err == nil && len(out) == 0 {
		return s.existing(ctx, key, hash)
	}
	return out, err
}

var internalFileLink = regexp.MustCompile(`\[([^\]]*)\]\(astral-file://[^)]+\)`)
var bareFileLink = regexp.MustCompile(`astral-file://[^\s<>)]+`)

func presentText(text string) string {
	text = internalFileLink.ReplaceAllString(text, `$1 (file available in Astral)`)
	return bareFileLink.ReplaceAllString(text, "[file available in Astral]")
}
func (s *Service) dispatchPending() {
	all, err := list[domain.Outbox](s.ctx, s.Store, "outbox")
	if err != nil {
		return
	}
	slices.SortStableFunc(all, func(a, b domain.Outbox) int {
		if a.GroupID == b.GroupID {
			return a.Fragment - b.Fragment
		}
		if v := a.CreatedAt.Compare(b.CreatedAt); v != 0 {
			return v
		}
		return strings.Compare(a.ID, b.ID)
	})
	blocked := map[string]bool{}
	failedGroups := map[string]bool{}
	for _, record := range all {
		if record.Status == "accepted" {
			continue
		}
		// Terminal errors stop dependent fragments, not unrelated later sends.
		// Unknown effects are never retried by the scheduler.
		if record.Status == "failed" || record.Status == "blocked" || record.Status == "unknown" {
			failedGroups[record.GroupID] = true
			continue
		}
		if failedGroups[record.GroupID] {
			continue
		}
		bucket := record.BindingID + ":" + record.ConversationID
		if blocked[bucket] {
			continue
		}
		blocked[bucket] = true
		if record.Status != "pending" {
			continue
		}
		s.mu.Lock()
		if s.deletingAgents[record.AgentID] {
			s.mu.Unlock()
			continue
		}
		if len(s.sending) >= 4 {
			s.mu.Unlock()
			return
		}
		if s.sending[bucket] {
			s.mu.Unlock()
			continue
		}
		s.sending[bucket] = true
		s.workers.Add(1)
		group := s.agentWorkersLocked(record.AgentID)
		group.workers.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.workers.Done()
			defer group.workers.Done()
			defer func() { s.mu.Lock(); delete(s.sending, bucket); s.mu.Unlock() }()
			_ = s.dispatch(group.ctx, record.ID)
		}()
	}
}
func (s *Service) authorizeOutbound(ctx context.Context, r domain.Outbox) (domain.Connection, error) {
	ctx = persistence.WithSessionScope(ctx, r.AgentID, r.SessionID)
	if _, _, _, err := s.target(ctx, r.AgentID, r.SessionID, r.ConversationID); err != nil {
		return domain.Connection{}, err
	}
	var binding domain.ConversationBinding
	var conversation domain.Conversation
	var connection domain.Connection
	if r.BindingID != "" {
		if err := s.Store.Get(ctx, "bindings", r.BindingID, &binding); err != nil {
			return connection, err
		}
		if !binding.Enabled || binding.AgentID != r.AgentID || binding.ConversationID != r.ConversationID {
			return connection, domain.Conflict("the original outbound binding is no longer active")
		}
	}
	if err := s.Store.Get(ctx, "conversations", r.ConversationID, &conversation); err != nil {
		return connection, err
	}
	if conversation.ConnectionID != r.ConnectionID || conversation.ExternalID != r.ExternalID || conversation.TopicID != r.TopicID {
		return connection, domain.Conflict("the frozen outbound destination has changed")
	}
	if err := s.Store.Get(ctx, "connections", r.ConnectionID, &connection); err != nil {
		return connection, err
	}
	if !connection.Enabled || connection.AccountID != r.ConnectionAccountID || connection.AgentID != r.AgentID {
		return connection, domain.Conflict("the original channel account is unavailable")
	}
	if r.BindingID == "" && connection.AccessMode != "all" {
		return connection, domain.Conflict("chat is no longer in this account's scope")
	}
	if r.ReplyTo != "" && connection.Platform == "telegram" {
		if err := s.telegramMessagePrivacy(ctx, r.AgentID, connection, conversation, r.ReplyTo); err != nil {
			return connection, err
		}
	}
	return connection, nil
}
func (s *Service) dispatch(ctx context.Context, id string) error {
	s.gate.Lock()
	var record domain.Outbox
	err := s.Store.Get(ctx, "outbox", id, &record)
	if err != nil || record.Status != "pending" {
		s.gate.Unlock()
		return err
	}
	connection, err := s.authorizeOutbound(ctx, record)
	if err != nil {
		record.Status, record.Error = "blocked", err.Error()
		err = s.Store.Save(ctx, "outbox", &record.Meta, &record)
		s.gate.Unlock()
		return err
	}
	adapter, err := s.adapter(ctx, connection)
	if err != nil {
		s.gate.Unlock()
		return err
	}
	// Commit dispatch before any external operation. A crash from here onwards
	// requires a receipt check, including before a source stream is opened.
	record.Status = "dispatching"
	err = s.Store.Save(ctx, "outbox", &record.Meta, &record)
	s.gate.Unlock()
	if err != nil {
		return err
	}
	send := Send{MessageOptions: record.MessageOptions, Target: domain.Conversation{ConnectionID: record.ConnectionID, ExternalID: record.ExternalID, TopicID: record.TopicID}, Text: record.Text, ReplyTo: record.ReplyTo}
	if len(record.Parts) > 0 && record.FileRefID == "" && (connection.Adapter == "telegram.bot" || connection.Adapter == "qq.napcat" || connection.Adapter == "feishu.bot") {
		for _, part := range record.Parts {
			prepared := SendPart{Type: part.Type, Text: part.Text, UserID: part.UserID, Name: part.Name}
			if part.FileRefID != "" {
				var file domain.FileRef
				if err = s.Store.Get(ctx, "files", part.FileRefID, &file); err != nil {
					return s.finish(ctx, &record, Receipt{}, &SendError{Message: err.Error()})
				}
				prepared.FileName, prepared.MediaType, prepared.Size = file.DisplayName, file.MediaType, file.Size
				prepared.OpenFile = func(openCtx context.Context) (io.ReadCloser, error) {
					_, _, body, openErr := s.Harness.Files.Read(openCtx, record.AgentID, record.SessionID, filestream.URL(file), 0, nil)
					return body, openErr
				}
			}
			send.Parts = append(send.Parts, prepared)
		}
	}
	if record.FileRefID != "" {
		var file domain.FileRef
		err = s.Store.Get(ctx, "files", record.FileRefID, &file)
		if err == nil {
			_, _, body, readErr := s.Harness.Files.Read(ctx, record.AgentID, record.SessionID, filestream.URL(file), 0, nil)
			err = readErr
			if err == nil {
				defer body.Close()
				send.File, send.FileName, send.MediaType, send.Size = body, file.DisplayName, file.MediaType, file.Size
				send.OpenFile = func(openCtx context.Context) (io.ReadCloser, error) {
					_, _, source, openErr := s.Harness.Files.Read(openCtx, record.AgentID, record.SessionID, filestream.URL(file), 0, nil)
					return source, openErr
				}
			}
		}
		if err != nil {
			return s.finish(ctx, &record, Receipt{}, &SendError{Message: err.Error()})
		}
	}
	receipt, sendErr := adapter.Send(ctx, send)
	return s.finish(ctx, &record, receipt, sendErr)
}
func (s *Service) finish(ctx context.Context, record *domain.Outbox, receipt Receipt, sendErr error) error {
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	record.Status, record.Error = "accepted", ""
	record.PlatformMessageIDs = receipt.MessageIDs
	if sendErr != nil {
		record.Status, record.Error = "unknown", sendErr.Error()
		var known *SendError
		if errors.As(sendErr, &known) && !known.Unknown {
			record.Status = "failed"
		}
	} else if len(receipt.MessageIDs) == 0 {
		record.Status, record.Error = "unknown", "platform response did not identify an accepted message"
	}
	return s.Store.Update(persist, func(tx *persistence.Tx) error {
		if err := tx.RequireActiveAgent(persist, record.AgentID); err != nil {
			return err
		}
		if err := tx.Save(persist, "outbox", &record.Meta, record); err != nil {
			return err
		}
		if record.Status != "accepted" {
			return nil
		}
		var connection domain.Connection
		if err := tx.Get(persist, "connections", record.ConnectionID, &connection); err != nil {
			return err
		}
		for _, messageID := range record.PlatformMessageIDs {
			event := domain.Observation{SchemaVersion: 1, ConnectionID: record.ConnectionID, ConversationID: record.ConversationID, Kind: "message.created", MessageID: messageID,
				Sender: domain.Sender{ID: connection.AccountID, Name: connection.Name, Username: connection.Username}, Self: true,
				Text: archiveMessageText(*record), Format: record.Format, ReplyID: record.ReplyTo, SentAt: time.Now().UTC(), ReceivedAt: time.Now().UTC()}
			if record.FileRefID != "" {
				var file domain.FileRef
				if err := tx.Get(persist, "files", record.FileRefID, &file); err != nil {
					return err
				}
				kind := "file"
				if strings.HasPrefix(file.MediaType, "image/") {
					kind = "image"
				}
				event.Attachments = []domain.Attachment{{Ref: "outbox:" + record.ID, Kind: kind, Name: file.DisplayName, MediaType: file.MediaType, Size: file.Size, FileURI: filestream.URL(file)}}
			}
			if len(record.Parts) > 0 {
				event.Text, event.Parts, event.Attachments = "", nil, nil
				for i, part := range record.Parts {
					ref := fmt.Sprintf("outbox:%s:%d", record.ID, i)
					if part.FileRefID != "" {
						var file domain.FileRef
						if err := tx.Get(persist, "files", part.FileRefID, &file); err != nil {
							return err
						}
						kind := part.Type
						attachment := domain.Attachment{Ref: ref, Kind: kind, Name: file.DisplayName, MediaType: file.MediaType, Size: file.Size, FileURI: filestream.URL(file)}
						event.Attachments = append(event.Attachments, attachment)
						event.Parts = append(event.Parts, domain.MessagePart{Ref: fmt.Sprintf("p%d", i), Type: "attachment", Attachment: &attachment})
					} else {
						text := part.Text
						if part.Type == "mention" {
							name := part.Name
							if name == "" {
								name = part.UserID
							}
							text = "@" + strings.TrimPrefix(name, "@")
						}
						event.Text += text
						event.Parts = append(event.Parts, domain.MessagePart{Ref: fmt.Sprintf("p%d", i), Type: "text", Text: text})
					}
				}
			}
			if _, err := tx.Archive(persist, normalizeSelf(event)); err != nil {
				return err
			}
		}
		return nil
	})
}

func normalizeSelf(event domain.Observation) domain.Observation {
	if event.Self && event.Kind == "message.created" {
		event.ExternalEventID = "self:" + event.ConversationID + ":" + event.MessageID
		event.ID = stableID("event", event.ConnectionID, event.ExternalEventID)
		event.Trigger = ""
	}
	return event
}

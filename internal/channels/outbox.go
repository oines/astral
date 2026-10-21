package channels

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	core "github.com/oines/astral-core"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/persistence"
)

type SendRequest struct {
	IntentID         string                   `json:"intent_id"`
	ConversationID   string                   `json:"conversation_id,omitempty"`
	Text             string                   `json:"text,omitempty"`
	FileURI          string                   `json:"file_uri,omitempty"`
	Path             string                   `json:"path,omitempty"`
	SourceComputerID string                   `json:"source_computer_id,omitempty"`
	AssistantEntryID string                   `json:"-"`
	BindingID        string                   `json:"-"`
	SourceBinding    *domain.ExecutionBinding `json:"-"`
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
	if request.IntentID == "" || len(request.IntentID) > 200 {
		return nil, domain.Invalid("a stable send intent is required")
	}
	key := stableID("send", agentID, sessionID, request.IntentID)
	hash := stableID("digest", string(encoded(request)))
	if existing, err := s.existing(ctx, key, hash); err == nil {
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
	var file domain.FileRef
	var fragments []string
	if request.FileURI != "" || request.Path != "" {
		if !adapter.Capabilities().FileSending {
			return nil, domain.Invalid("this adapter cannot send files")
		}
		if request.FileURI != "" && request.Path != "" {
			return nil, domain.Invalid("provide a file URI or source path, not both")
		}
		if request.Path != "" {
			source := request.SourceBinding
			if source == nil || source.AgentID != agentID || source.SessionID != sessionID || source.ComputerID != request.SourceComputerID {
				return nil, domain.Forbidden("file path needs the authenticated source Computer")
			}
			file, err = s.Harness.Files.Reference(ctx, *source, request.Path, true)
		} else {
			file, err = s.Harness.Files.Get(ctx, agentID, request.FileURI)
		}
		if err != nil {
			return nil, err
		}
		if file.Version == "" {
			source, bindErr := s.Harness.Hub.Bind(ctx, agentID, sessionID, file.ComputerID, "")
			if bindErr != nil {
				return nil, bindErr
			}
			file, err = s.Harness.Files.Reference(ctx, source, file.Path, true)
			if err != nil {
				return nil, err
			}
		}
		if file.Size > adapter.Capabilities().UploadLimit {
			return nil, domain.Invalid("file exceeds the selected adapter's upload limit")
		}
		fragments = []string{request.Text}
	} else {
		fragments, err = adapter.SplitText(presentText(request.Text))
		if err != nil {
			return nil, err
		}
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
		for index, text := range fragments {
			id := firstID
			if index > 0 {
				id = domain.ID("outbox")
			}
			record := domain.Outbox{Meta: domain.Meta{ID: id}, GroupID: key, IntentKey: fmt.Sprintf("%s:%d", key, index), RequestDigest: hash,
				AgentID: agentID, SessionID: sessionID, BindingID: binding.ID, ConversationID: conversation.ID, ConnectionID: connection.ID, ConnectionAccountID: connection.AccountID,
				ExternalID: conversation.ExternalID, TopicID: conversation.TopicID, AssistantEntryID: request.AssistantEntryID, Fragment: index, Text: text, FileRefID: file.ID, Status: "pending"}
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
func (s *Service) collect(ctx context.Context, b domain.ConversationBinding) error {
	session, err := s.Harness.Core(ctx, b.SessionID)
	if err != nil {
		return err
	}
	position := cursor{Meta: domain.Meta{ID: b.ID}}
	if err = s.Store.Get(ctx, "outbox_cursors", b.ID, &position); err != nil && !domain.IsMissing(err) {
		return err
	}
	page, err := session.History().Scan(ctx, core.Query{SessionID: b.SessionID, Kind: core.EntryMessage, Cursor: position.Value, Limit: 100, MaxBytes: 64 << 20})
	if err != nil {
		return err
	}
	previousSequence, previousCursor := position.Sequence, position.Value
	for _, entry := range page.Entries {
		if entry.Sequence <= uint64(position.Sequence) {
			continue
		}
		if entry.Message != nil && entry.Message.Role == core.RoleAssistant && !entry.Message.Failed() {
			var text strings.Builder
			for _, part := range entry.Message.Content {
				if part.Type == "text" {
					text.WriteString(part.Text)
				}
			}
			if strings.TrimSpace(text.String()) != "" {
				if _, err = s.Enqueue(ctx, b.AgentID, b.SessionID, SendRequest{IntentID: "assistant:" + entry.ID, ConversationID: b.ConversationID, Text: text.String(), AssistantEntryID: entry.ID, BindingID: b.ID}); err != nil {
					return err
				}
			}
		}
		position.Sequence = int64(entry.Sequence)
	}
	// Core's terminal cursor may be empty; retain the last page cursor and
	// sequence so appends are observed without missing the previous tail.
	if page.NextCursor != "" {
		position.Value = page.NextCursor
	}
	if position.Sequence == previousSequence && position.Value == previousCursor {
		return nil
	}
	return s.Store.Save(ctx, "outbox_cursors", &position.Meta, &position)
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
	for _, record := range all {
		if record.Status == "accepted" {
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
		s.mu.Unlock()
		go func() {
			defer s.workers.Done()
			defer func() { s.mu.Lock(); delete(s.sending, bucket); s.mu.Unlock() }()
			_ = s.dispatch(s.ctx, record.ID)
		}()
	}
}
func (s *Service) authorizeOutbound(ctx context.Context, r domain.Outbox) (domain.Connection, error) {
	var binding domain.ConversationBinding
	var conversation domain.Conversation
	var connection domain.Connection
	if err := s.Store.Get(ctx, "bindings", r.BindingID, &binding); err != nil {
		return connection, err
	}
	if !binding.Enabled || binding.AgentID != r.AgentID || binding.ConversationID != r.ConversationID {
		return connection, domain.Conflict("the original outbound binding is no longer active")
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
	if !connection.Enabled || connection.AccountID != r.ConnectionAccountID {
		return connection, domain.Conflict("the original channel account is unavailable")
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
	send := Send{Target: domain.Conversation{ConnectionID: record.ConnectionID, ExternalID: record.ExternalID, TopicID: record.TopicID}, Text: record.Text}
	if record.FileRefID != "" {
		var file domain.FileRef
		err = s.Store.Get(ctx, "files", record.FileRefID, &file)
		if err == nil {
			_, _, body, readErr := s.Harness.Files.Read(ctx, record.AgentID, record.SessionID, filestream.URL(file), 0, nil)
			err = readErr
			if err == nil {
				defer body.Close()
				send.File, send.FileName, send.MediaType, send.Size = body, file.DisplayName, file.MediaType, file.Size
			}
		}
		if err != nil {
			return s.finish(ctx, &record, Receipt{}, &SendError{Message: err.Error(), Retryable: true})
		}
	}
	receipt, sendErr := adapter.Send(ctx, send)
	return s.finish(ctx, &record, receipt, sendErr)
}
func (s *Service) finish(ctx context.Context, record *domain.Outbox, receipt Receipt, sendErr error) error {
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	record.Status, record.Error, record.Retryable = "accepted", "", false
	record.PlatformMessageIDs = receipt.MessageIDs
	if sendErr != nil {
		record.Status, record.Error = "unknown", sendErr.Error()
		var known *SendError
		if errors.As(sendErr, &known) && !known.Unknown {
			record.Status, record.Retryable = "failed", known.Retryable
		}
	} else if len(receipt.MessageIDs) == 0 {
		record.Status, record.Error = "unknown", "platform response did not identify an accepted message"
	}
	return s.Store.Save(persist, "outbox", &record.Meta, record)
}
func (s *Service) Retry(ctx context.Context, id string) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	var record domain.Outbox
	if err := s.Store.Get(ctx, "outbox", id, &record); err != nil {
		return err
	}
	if record.Status != "failed" || !record.Retryable {
		return domain.Conflict("only a confirmed, retryable failure can be retried")
	}
	if _, err := s.authorizeOutbound(ctx, record); err != nil {
		return err
	}
	record.Status, record.Error, record.Retryable = "pending", "", false
	return s.Store.Save(ctx, "outbox", &record.Meta, &record)
}
func (s *Service) Resolve(ctx context.Context, id, status, note string, messageIDs []string) error {
	if status != "accepted" && status != "failed" {
		return domain.Invalid("resolution must be accepted or failed")
	}
	if strings.TrimSpace(note) == "" {
		return domain.Invalid("record the evidence used to resolve this send")
	}
	s.gate.Lock()
	defer s.gate.Unlock()
	var record domain.Outbox
	if err := s.Store.Get(ctx, "outbox", id, &record); err != nil {
		return err
	}
	if record.Status != "unknown" && record.Status != "blocked" {
		return domain.Conflict("only unknown or blocked sends need manual resolution")
	}
	record.Status, record.ResolutionNote, record.PlatformMessageIDs, record.Error = status, note, messageIDs, ""
	record.Retryable = status == "failed"
	return s.Store.Save(ctx, "outbox", &record.Meta, &record)
}

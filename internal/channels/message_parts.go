package channels

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
)

const messagePartTimeout = 15 * time.Second

// MessagePartPage retains the selected part's absolute path. For a forward,
// Messages is the requested page and Total is the source's full message count.
// Source handles and raw payloads are trusted internal data; HTTP projections
// must remove them unless the caller explicitly requests raw card content.
type MessagePartPage struct {
	Part       domain.MessagePart `json:"part"`
	Offset     int                `json:"offset"`
	NextOffset *int               `json:"next_offset,omitempty"`
}

type messagePartResolver struct {
	service    *Service
	binding    domain.ExecutionBinding
	chat       domain.Conversation
	connection domain.Connection
	messageID  string
	adapter    Adapter
	bytesLeft  int64
	callsLeft  int
	nodesLeft  int
	current    []domain.MessagePart
	refreshed  bool
	forwards   map[string]json.RawMessage
	parentID   string
	quoted     bool
}

func (s *Service) messagePartResolver(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID string) (*messagePartResolver, []domain.MessagePart, error) {
	_, chat, connection, err := s.target(ctx, b.AgentID, b.SessionID, conversationID)
	if err != nil {
		return nil, nil, err
	}
	message, quoted, err := s.Store.MessagePartsSource(ctx, chat.ID, messageID)
	if err != nil {
		return nil, nil, err
	}
	if message.Kind == "message.deleted" {
		return nil, nil, domain.Missing("message", messageID)
	}
	if message.ConnectionID != connection.ID || message.ConversationID != chat.ID {
		return nil, nil, domain.Forbidden("message is outside the selected account and chat")
	}
	resolver := &messagePartResolver{service: s, binding: b, chat: chat, connection: connection, messageID: messageID, bytesLeft: 4 << 20, callsLeft: 16, nodesLeft: 2048, forwards: map[string]json.RawMessage{}}
	resolver.parentID, resolver.quoted = message.MessageID, quoted
	parts := message.Parts
	if quoted {
		parts = message.ReplyParts
	}
	if len(parts) != 0 {
		payload, err := json.Marshal(parts)
		if err != nil || int64(len(payload)) > resolver.bytesLeft {
			return nil, nil, domain.Invalid("archived message parts exceed the resource size budget")
		}
		resolver.bytesLeft -= int64(len(payload))
	}
	if len(parts) == 0 && connection.Adapter == "qq.napcat" {
		adapter, err := resolver.sourceAdapter(ctx)
		if err != nil {
			return nil, nil, err
		}
		n, ok := adapter.(*napcat)
		if !ok {
			return nil, nil, domain.Invalid("message parts require the account's NapCat adapter")
		}
		raw, err := resolver.napcatCall(ctx, n, "get_msg", map[string]any{"message_id": messageID})
		if err != nil {
			return nil, nil, err
		}
		if err = resolver.napcatScope(ctx, n, raw, messageID); err != nil {
			return nil, nil, err
		}
		var source struct {
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(raw, &source) != nil {
			return nil, nil, domain.Offline("NapCat returned invalid message parts")
		}
		parts, _, _ = napcatParts(source.Message, messageID, "")
		resolver.current, resolver.refreshed = parts, true
	} else if len(parts) == 0 {
		parts = []domain.MessagePart{}
		if message.Text != "" {
			parts = append(parts, domain.MessagePart{Ref: "p0", Type: "text", Text: message.Text})
		}
		for _, attachment := range message.Attachments {
			copy := attachment
			parts = append(parts, domain.MessagePart{Ref: "p" + strconv.Itoa(len(parts)), Type: "attachment", Attachment: &copy})
		}
	}
	if err := resolver.consumeParts(parts, 0); err != nil {
		return nil, nil, err
	}
	return resolver, parts, nil
}

func (r *messagePartResolver) consumeParts(parts []domain.MessagePart, depth int) error {
	if depth > 6 {
		return domain.Invalid("message parts exceed the six-level forward depth budget")
	}
	for _, part := range parts {
		r.nodesLeft--
		if r.nodesLeft < 0 {
			return domain.Invalid("message parts exceeded the request's traversal node budget")
		}
		if part.Forward != nil {
			for _, message := range part.Forward.Messages {
				if err := r.consumeParts(message.Parts, depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *messagePartResolver) sourceAdapter(ctx context.Context) (Adapter, error) {
	if r.adapter == nil {
		var err error
		r.adapter, err = r.service.adapter(ctx, r.connection)
		if err != nil {
			return nil, err
		}
	}
	return r.adapter, nil
}

func (r *messagePartResolver) authorize(ctx context.Context) error {
	return r.service.authorizeMessagePart(ctx, r.binding, r.chat.ID, r.messageID, r.connection.ID)
}

func (s *Service) authorizeMessagePart(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID, connectionID string) error {
	_, _, current, err := s.target(ctx, b.AgentID, b.SessionID, conversationID)
	if err == nil && current.ID != connectionID {
		return domain.Forbidden("chat account changed during message resource access")
	}
	if err != nil {
		return err
	}
	source, _, err := s.Store.MessagePartsSource(ctx, conversationID, messageID)
	if err != nil {
		return err
	}
	if source.Kind == "message.deleted" {
		return domain.Missing("message", messageID)
	}
	if source.ConnectionID != connectionID || source.ConversationID != conversationID {
		return domain.Forbidden("message source changed during resource access")
	}
	return nil
}

func messagePartPath(ref string) ([]string, error) {
	path := strings.Split(ref, ".")
	if len(path) == 0 || len(path) > 13 || len(path)%2 != 1 {
		return nil, domain.Invalid("part ref must be pN followed by at most six .mN.pN levels")
	}
	for i, value := range path {
		prefix := byte('p')
		if i%2 == 1 {
			prefix = 'm'
		}
		if len(value) < 2 || value[0] != prefix || len(value) > 10 || len(value) > 2 && value[1] == '0' {
			return nil, domain.Invalid("invalid message part ref")
		}
		for _, digit := range value[1:] {
			if digit < '0' || digit > '9' {
				return nil, domain.Invalid("invalid message part ref")
			}
		}
	}
	return path, nil
}

func (r *messagePartResolver) selectPart(ctx context.Context, parts []domain.MessagePart, ref string) (domain.MessagePart, error) {
	path, err := messagePartPath(ref)
	if err != nil {
		return domain.MessagePart{}, err
	}
	for depth := 0; depth < len(path); depth += 2 {
		prefix := strings.Join(path[:depth+1], ".")
		var found *domain.MessagePart
		for i := range parts {
			if parts[i].Ref == prefix {
				found = &parts[i]
				break
			}
		}
		if found == nil {
			return domain.MessagePart{}, domain.Missing("message part", ref)
		}
		if depth == len(path)-1 {
			return *found, nil
		}
		if found.Type != "forward" || found.Forward == nil {
			return domain.MessagePart{}, domain.Invalid("part path crosses a non-forward part")
		}
		index, _ := strconv.Atoi(path[depth+1][1:])
		messages, _, err := r.forwardPage(ctx, *found, index, 1)
		if err != nil {
			return domain.MessagePart{}, err
		}
		if len(messages) == 0 {
			return domain.MessagePart{}, domain.Missing("forwarded message", path[depth+1])
		}
		parts = messages[0].Parts
	}
	return domain.MessagePart{}, domain.Missing("message part", ref)
}

// MessageParts discovers the archived parent message's ordered root parts.
// Reading an old message may query the source but never rewrites IM history.
func (s *Service) MessageParts(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID string) ([]domain.MessagePart, error) {
	ctx, cancel := context.WithTimeout(ctx, messagePartTimeout)
	defer cancel()
	resolver, parts, err := s.messagePartResolver(ctx, b, conversationID, messageID)
	if err == nil {
		err = resolver.authorize(ctx)
	}
	return parts, err
}

func (s *Service) MessagePart(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID, ref string, offset, limit int) (MessagePartPage, error) {
	if offset < 0 || limit < 0 || limit > 50 {
		return MessagePartPage{}, domain.Invalid("offset must be nonnegative and limit must be between 1 and 50")
	}
	if limit == 0 {
		limit = 20
	}
	if _, err := messagePartPath(ref); err != nil {
		return MessagePartPage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, messagePartTimeout)
	defer cancel()
	resolver, parts, err := s.messagePartResolver(ctx, b, conversationID, messageID)
	if err != nil {
		return MessagePartPage{}, err
	}
	part, err := resolver.selectPart(ctx, parts, ref)
	if err != nil {
		return MessagePartPage{}, err
	}
	page := MessagePartPage{Part: part, Offset: offset}
	if part.Forward != nil {
		messages, total, err := resolver.forwardPage(ctx, part, offset, limit)
		if err != nil {
			return MessagePartPage{}, err
		}
		forward := *part.Forward
		forward.Messages, forward.Total = messages, total
		forward.Status = "inline"
		if offset != 0 || len(messages) < total {
			forward.Status = "truncated"
		}
		page.Part.Forward = &forward
		if next := offset + len(messages); next < total {
			page.NextOffset = &next
		}
	} else if offset != 0 {
		return MessagePartPage{}, domain.Invalid("offset only applies to forward parts")
	}
	return page, resolver.authorize(ctx)
}

// MessagePartAttachment exposes a trusted source only after parent-chat and
// nested-path verification. It never treats forwarded chat IDs as permission.
func (s *Service) MessagePartAttachment(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID, ref string) (domain.Attachment, error) {
	part, _, err := s.resolveMessagePart(ctx, b, conversationID, messageID, ref)
	if err != nil {
		return domain.Attachment{}, err
	}
	if part.Type != "attachment" || part.Attachment == nil {
		return domain.Attachment{}, domain.Invalid("selected message part is not an attachment")
	}
	return *part.Attachment, nil
}

func (s *Service) MessagePartRaw(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID, ref string) (json.RawMessage, error) {
	part, _, err := s.resolveMessagePart(ctx, b, conversationID, messageID, ref)
	if err != nil {
		return nil, err
	}
	if part.Card == nil || len(part.Card.Raw) == 0 {
		return nil, domain.Invalid("selected message part has no raw card payload")
	}
	return part.Card.Raw, nil
}

func (s *Service) resolveMessagePart(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID, ref string) (domain.MessagePart, domain.Connection, error) {
	if _, err := messagePartPath(ref); err != nil {
		return domain.MessagePart{}, domain.Connection{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, messagePartTimeout)
	defer cancel()
	resolver, parts, err := s.messagePartResolver(ctx, b, conversationID, messageID)
	if err != nil {
		return domain.MessagePart{}, domain.Connection{}, err
	}
	part, err := resolver.selectPart(ctx, parts, ref)
	if err == nil {
		err = resolver.authorize(ctx)
	}
	return part, resolver.connection, err
}

func (s *Service) MessagePartContent(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID, ref string) (Download, error) {
	part, connection, err := s.resolveMessagePart(ctx, b, conversationID, messageID, ref)
	if err != nil {
		return Download{}, err
	}
	if part.Type != "attachment" || part.Attachment == nil {
		return Download{}, domain.Invalid("selected message part is not an attachment")
	}
	attachment := *part.Attachment
	download, err := s.downloadSource(ctx, b.AgentID, b.SessionID, attachment, connection)
	if err != nil {
		return Download{}, err
	}
	if err = s.authorizeMessagePart(ctx, b, conversationID, messageID, connection.ID); err != nil {
		_ = download.Body.Close()
		return Download{}, err
	}
	if download.MediaType == "" {
		download.MediaType = attachment.MediaType
	}
	return download, nil
}

func (s *Service) DownloadMessagePart(ctx context.Context, b domain.ExecutionBinding, conversationID, messageID, ref, destination string) (domain.FileRef, error) {
	if strings.TrimSpace(destination) == "" {
		return domain.FileRef{}, domain.Invalid("an explicit destination path is required")
	}
	download, err := s.MessagePartContent(ctx, b, conversationID, messageID, ref)
	if err != nil {
		return domain.FileRef{}, err
	}
	defer download.Body.Close()
	_, body, err := s.Harness.Hub.Call(ctx, "file.write", computers.Call{Binding: b, Path: destination}, download.Body)
	if err != nil {
		return domain.FileRef{}, err
	}
	_, copyErr := io.Copy(io.Discard, body)
	if err = errors.Join(copyErr, body.Close()); err != nil {
		return domain.FileRef{}, err
	}
	_, _, connection, err := s.target(ctx, b.AgentID, b.SessionID, conversationID)
	if err != nil {
		return domain.FileRef{}, err
	}
	if err = s.authorizeMessagePart(ctx, b, conversationID, messageID, connection.ID); err != nil {
		return domain.FileRef{}, err
	}
	return s.Harness.Files.Reference(ctx, b, destination, false)
}

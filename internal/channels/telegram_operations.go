package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
)

// Parameters follow the linked Bot API method. Astral supplies chat_id and the
// selected Topic for chat operations. Account-scoped resource reads do not need
// a conversation. Files maps multipart field names to Computer paths or FileRefs.
type TelegramRequest struct {
	ConversationID       string            `json:"conversation_id,omitempty"`
	SourceConversationID string            `json:"source_conversation_id,omitempty"`
	Parameters           map[string]any    `json:"parameters"`
	Files                map[string]string `json:"files,omitempty"`
}
type TelegramResult struct {
	Result json.RawMessage `json:"result"`
}

func (s *Service) telegramAccount(ctx context.Context, agentID, id string) (*telegram, domain.Connection, error) {
	var c domain.Connection
	if err := s.Store.Get(ctx, "connections", id, &c); err != nil {
		return nil, c, err
	}
	if c.AgentID != agentID {
		return nil, c, domain.Forbidden("account is outside the current Agent view")
	}
	if !c.Enabled {
		return nil, c, domain.Offline("account is disabled")
	}
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", agentID, &agent); err != nil {
		return nil, c, err
	}
	if agent.Deletion {
		return nil, c, domain.Missing("Bot", agentID)
	}
	a, err := s.adapter(ctx, c)
	if err != nil {
		return nil, c, err
	}
	t, ok := a.(*telegram)
	if !ok {
		return nil, c, domain.Invalid("this account does not support Telegram operations")
	}
	return t, c, nil
}
func (s *Service) TelegramOperations(ctx context.Context, agentID, accountID string) ([]TelegramOperation, error) {
	_, _, err := s.telegramAccount(ctx, agentID, accountID)
	return telegramOperations, err
}

func (s *Service) TelegramOperation(ctx context.Context, b domain.ExecutionBinding, accountID, method string, request TelegramRequest) (TelegramResult, error) {
	index := slices.IndexFunc(telegramOperations, func(v TelegramOperation) bool { return v.Name == method })
	if index < 0 {
		return TelegramResult{}, domain.Invalid("unknown chat operation; inspect the account's operations catalog")
	}
	operation := telegramOperations[index]
	if operation.AccountScoped {
		return s.telegramResourceOperation(ctx, b, accountID, method, request)
	}
	t, connection, err := s.telegramAccount(ctx, b.AgentID, accountID)
	if err != nil {
		return TelegramResult{}, err
	}
	_, target, owner, err := s.target(ctx, b.AgentID, b.SessionID, request.ConversationID)
	if err != nil {
		return TelegramResult{}, err
	}
	if owner.ID != accountID {
		return TelegramResult{}, domain.Forbidden("chat belongs to another account")
	}
	parameters := request.Parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	for name := range parameters {
		if !slices.Contains(operation.Parameters, name) {
			return TelegramResult{}, domain.Invalid("unsupported operation parameter: " + name)
		}
	}
	// These alternate addressing mechanisms bypass the selected Bot chat. They
	// need independent account/inline grants, which this chat API does not issue.
	for _, name := range []string{"business_connection_id", "inline_message_id"} {
		if value, ok := parameters[name]; ok && value != "" && value != nil {
			return TelegramResult{}, domain.Forbidden(name + " is outside the Bot chat scope")
		}
	}
	if method == "sendGift" && parameters["user_id"] != nil {
		return TelegramResult{}, domain.Invalid("sendGift uses the selected chat, not an alternate user_id")
	}
	if raw, ok := parameters["chat_id"]; ok && telegramID(raw) != target.ExternalID {
		return TelegramResult{}, domain.Forbidden("chat_id does not match the selected conversation")
	}
	if slices.Contains(operation.Parameters, "chat_id") {
		parameters["chat_id"] = target.ExternalID
	}
	if raw, ok := parameters["message_thread_id"]; ok && telegramID(raw) != target.TopicID {
		return TelegramResult{}, domain.Forbidden("select the target Topic as a conversation")
	}
	if target.TopicID != "" && slices.Contains(operation.Parameters, "message_thread_id") {
		parameters["message_thread_id"], _ = strconv.ParseInt(target.TopicID, 10, 64)
	}
	if parameters["direct_messages_topic_id"] != nil {
		return TelegramResult{}, domain.Invalid("direct-message Topics require an independently resolved conversation")
	}
	if slices.Contains(operation.Parameters, "scope") {
		scope, _ := parameters["scope"].(map[string]any)
		if scope == nil {
			scope = map[string]any{"type": "chat"}
		}
		if scope["type"] != "chat" && scope["type"] != "chat_member" {
			return TelegramResult{}, domain.Forbidden("command scope must target the selected chat")
		}
		if id, ok := scope["chat_id"]; ok && telegramID(id) != target.ExternalID {
			return TelegramResult{}, domain.Forbidden("command scope targets another chat")
		}
		scope["chat_id"] = target.ExternalID
		parameters["scope"] = scope
	}
	source := target
	if slices.Contains(operation.Parameters, "from_chat_id") {
		if request.SourceConversationID == "" {
			return TelegramResult{}, domain.Invalid("source_conversation_id is required")
		}
		_, source, owner, err = s.target(ctx, b.AgentID, b.SessionID, request.SourceConversationID)
		if err != nil {
			return TelegramResult{}, err
		}
		if owner.ID != accountID {
			return TelegramResult{}, domain.Forbidden("source chat belongs to another account")
		}
		if id, ok := parameters["from_chat_id"]; ok && telegramID(id) != source.ExternalID {
			return TelegramResult{}, domain.Forbidden("from_chat_id does not match the selected source")
		}
		parameters["from_chat_id"] = source.ExternalID
	}
	// A Telegram message ID is only chat-wide. For a Topic-only grant, require
	// the archived fact proving membership before editing, deleting or copying it.
	if source.TopicID != "" {
		for _, key := range []string{"message_id", "message_ids"} {
			if err := s.telegramMessageScope(ctx, source, parameters[key]); err != nil {
				return TelegramResult{}, err
			}
		}
		if !slices.Contains(operation.Parameters, "message_thread_id") && parameters["message_id"] == nil && parameters["message_ids"] == nil {
			return TelegramResult{}, domain.Forbidden("this operation requires access to the parent chat")
		}
	}
	if err := s.telegramNestedScope(ctx, b.AgentID, connection, target, parameters, true); err != nil {
		return TelegramResult{}, err
	}
	uploads := map[string]Download{}
	defer func() {
		for _, file := range uploads {
			_ = file.Body.Close()
		}
	}()
	for field, location := range request.Files {
		if strings.ContainsAny(field, "\r\n\"/\\") || field == "" {
			return TelegramResult{}, domain.Invalid("invalid upload field")
		}
		if !strings.HasPrefix(location, "astral-file://") {
			ref, err := s.Harness.Files.Reference(ctx, b, location, true)
			if err != nil {
				return TelegramResult{}, err
			}
			location = filestream.URL(ref)
		}
		ref, _, body, err := s.Harness.Files.Read(ctx, b.AgentID, b.SessionID, location, 0, nil)
		if err != nil {
			return TelegramResult{}, err
		}
		uploads[field] = Download{Body: body, Size: ref.Size, Name: ref.DisplayName}
		if slices.Contains(operation.Parameters, field) {
			parameters[field] = "attach://" + field
		}
	}
	// Recheck all authorized resources after resolving/uploading local sources.
	if err := s.AuthorizeConversation(ctx, b.AgentID, target.ID); err != nil {
		return TelegramResult{}, err
	}
	if source.ID != target.ID {
		if err := s.AuthorizeConversation(ctx, b.AgentID, source.ID); err != nil {
			return TelegramResult{}, err
		}
	}
	var result json.RawMessage
	if err := t.operation(ctx, method, parameters, uploads, &result); err != nil {
		return TelegramResult{}, err
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.archiveTelegramResult(persist, t, target, method, parameters, result); err != nil {
		return TelegramResult{Result: result}, err
	}
	return TelegramResult{Result: result}, nil
}

func telegramID(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return ""
}
func (s *Service) telegramMessageScope(ctx context.Context, chat domain.Conversation, value any) error {
	if value == nil {
		return nil
	}
	if ids, ok := value.([]any); ok {
		for _, id := range ids {
			if err := s.telegramMessageScope(ctx, chat, id); err != nil {
				return err
			}
		}
		return nil
	}
	_, err := s.Store.Message(ctx, chat.ID, telegramID(value))
	if err != nil {
		return domain.Forbidden("message is not known in the selected Topic")
	}
	return nil
}
func (s *Service) telegramNestedScope(ctx context.Context, agentID string, account domain.Connection, target domain.Conversation, value any, root bool) error {
	switch v := value.(type) {
	case map[string]any:
		chat := target
		for key, raw := range v {
			if !strings.HasSuffix(key, "chat_id") || root && (key == "chat_id" || key == "from_chat_id") {
				continue
			}
			id := telegramID(raw)
			if id == "" {
				return domain.Invalid("chat references must be platform IDs or usernames")
			}
			if id == target.ExternalID {
				continue
			}
			resolved, err := s.ResolveForAgent(ctx, agentID, account.ID, id, "")
			if err != nil {
				return err
			}
			if key == "chat_id" {
				chat = resolved
			}
		}
		if !root && chat.TopicID != "" {
			if err := s.telegramMessageScope(ctx, chat, v["message_id"]); err != nil {
				return err
			}
		}
		for _, child := range v {
			if err := s.telegramNestedScope(ctx, agentID, account, chat, child, false); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := s.telegramNestedScope(ctx, agentID, account, target, child, false); err != nil {
				return err
			}
		}
	}
	return nil
}
func (t *telegram) operation(ctx context.Context, method string, parameters map[string]any, files map[string]Download, result *json.RawMessage) error {
	if len(files) == 0 {
		return t.call(ctx, method, parameters, result)
	}
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	done := make(chan error, 1)
	go func() {
		var err error
		for key, value := range parameters {
			text, ok := value.(string)
			if !ok {
				text = string(encoded(value))
			}
			if err = form.WriteField(key, text); err != nil {
				break
			}
		}
		if err == nil {
			for field, file := range files {
				var part io.Writer
				part, err = form.CreateFormFile(field, file.Name)
				if err == nil {
					_, err = io.Copy(part, file.Body)
				}
				if err != nil {
					break
				}
			}
		}
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
		done <- err
	}()
	closeSources := func() {
		_ = reader.Close()
		for _, file := range files {
			_ = file.Body.Close()
		}
	}
	stop := context.AfterFunc(ctx, closeSources)
	err := t.request(ctx, method, reader, form.FormDataContentType(), result)
	closeSources()
	stop()
	copyErr := <-done
	if err != nil {
		return err
	}
	if len(*result) == 0 {
		return copyErr
	}
	return nil
}
func (s *Service) archiveTelegramResult(ctx context.Context, t *telegram, target domain.Conversation, method string, parameters map[string]any, raw json.RawMessage) error {
	var values []json.RawMessage
	if len(raw) > 0 && raw[0] == '[' {
		_ = json.Unmarshal(raw, &values)
	} else {
		values = []json.RawMessage{raw}
	}
	for _, value := range values {
		var message telegramMessage
		if json.Unmarshal(value, &message) != nil || message.ID == 0 {
			continue
		}
		if message.Chat.ID == 0 {
			message.Chat.ID, _ = strconv.ParseInt(target.ExternalID, 10, 64)
			message.Chat.Type = target.Kind
			message.Chat.Title = target.Title
			message.ThreadID, _ = strconv.ParseInt(target.TopicID, 10, 64)
			message.IsTopicMessage = target.TopicID != ""
			message.Date = time.Now().Unix()
			message.Text, _ = parameters["text"].(string)
		}
		event := t.normalize(0, message, strings.HasPrefix(method, "editMessage"))
		event.Observation.Self = true
		event.Observation.Trigger = ""
		event.Observation.ExternalEventID = stableID("operation", method, string(value))
		event.Observation.ID = stableID("event", t.connection.ID, event.Observation.ExternalEventID)
		if _, err := s.archiveEvent(ctx, normalizeSelf(event.Observation)); err != nil {
			return err
		}
	}
	if (method == "deleteMessage" || method == "deleteMessages") && bytes.Equal(raw, []byte("true")) {
		ids, _ := parameters["message_ids"].([]any)
		if id, ok := parameters["message_id"]; ok {
			ids = append(ids, id)
		}
		for _, id := range ids {
			messageID := telegramID(id)
			eventID := stableID("operation", target.ID, "deleted", messageID)
			event := domain.Observation{SchemaVersion: 1, ID: stableID("event", target.ConnectionID, eventID), ExternalEventID: eventID, ConnectionID: target.ConnectionID, ConversationID: target.ID, MessageID: messageID, Kind: "message.deleted", ReceivedAt: time.Now().UTC(), SentAt: time.Now().UTC(), Text: fmt.Sprintf("Message %s deleted", messageID)}
			if _, err := s.archiveEvent(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

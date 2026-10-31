package channels

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/oines/astral/internal/domain"
)

// Named operations expose explicitly scoped protocol capabilities. The account
// selects the protocol; the caller cannot supply a URL or alternate destination.
type ChatOperation struct {
	Name          string   `json:"name"`
	Parameters    []string `json:"parameters"`
	FileFields    []string `json:"file_fields,omitempty"`
	MessageScoped bool     `json:"message_scoped"`
	AccountScoped bool     `json:"account_scoped,omitempty"`
	Documentation string   `json:"documentation"`
}
type ChatOperationRequest = TelegramRequest
type ChatOperationResult struct {
	Result json.RawMessage `json:"result"`
}

func (s *Service) operationAccount(ctx context.Context, agentID, id string) (Adapter, domain.Connection, error) {
	var c domain.Connection
	if err := s.Store.Get(ctx, "connections", id, &c); err != nil {
		return nil, c, err
	}
	if c.AgentID != agentID {
		return nil, c, domain.Forbidden("account is outside the current Bot view")
	}
	if !c.Enabled {
		return nil, c, domain.Offline("account is disabled")
	}
	a, err := s.adapter(ctx, c)
	return a, c, err
}
func (s *Service) AccountOperations(ctx context.Context, agentID, accountID string) ([]ChatOperation, error) {
	a, _, err := s.operationAccount(ctx, agentID, accountID)
	if err != nil {
		return nil, err
	}
	return chatOperations(a), nil
}
func chatOperations(adapter Adapter) []ChatOperation {
	result := []ChatOperation{}
	switch adapter.(type) {
	case *weixinAdapter:
		result = append(result, WeixinOperationsCatalog()...)
	case *telegram:
		for _, op := range telegramOperations {
			result = append(result, ChatOperation{Name: op.Name, Parameters: op.Parameters, AccountScoped: op.AccountScoped, Documentation: op.Documentation})
		}
	case *feishu:
		for _, op := range FeishuOperationsCatalog() {
			result = append(result, ChatOperation{Name: op.Name, Parameters: op.Parameters, MessageScoped: op.MessageScoped, Documentation: op.Documentation})
		}
	case *napcat:
		result = append(result, NapCatOperationsCatalog()...)
	}
	return result
}
func (s *Service) AccountOperation(ctx context.Context, b domain.ExecutionBinding, accountID, name string, request ChatOperationRequest) (ChatOperationResult, error) {
	a, _, err := s.operationAccount(ctx, b.AgentID, accountID)
	if err != nil {
		return ChatOperationResult{}, err
	}
	if _, ok := a.(*telegram); ok {
		result, err := s.TelegramOperation(ctx, b, accountID, name, request)
		return ChatOperationResult(result), err
	}
	if request.SourceConversationID != "" {
		return ChatOperationResult{}, domain.Invalid("this operation does not accept source chats")
	}
	if _, napcatFiles := a.(*napcat); len(request.Files) != 0 && !napcatFiles {
		return ChatOperationResult{}, domain.Invalid("this operation does not accept file uploads")
	}
	operations := chatOperations(a)
	index := slices.IndexFunc(operations, func(op ChatOperation) bool { return op.Name == name })
	if index < 0 {
		return ChatOperationResult{}, domain.Invalid("unknown chat operation; inspect the account operations catalog")
	}
	op := operations[index]
	_, chat, owner, err := s.target(ctx, b.AgentID, b.SessionID, request.ConversationID)
	if err != nil {
		return ChatOperationResult{}, err
	}
	if owner.ID != accountID {
		return ChatOperationResult{}, domain.Forbidden("chat belongs to another account")
	}
	params := request.Parameters
	if params == nil {
		params = map[string]any{}
	}
	for key := range params {
		if !slices.Contains(op.Parameters, key) {
			return ChatOperationResult{}, domain.Invalid("unsupported operation parameter: " + key)
		}
	}
	for field := range request.Files {
		if !slices.Contains(op.FileFields, field) {
			return ChatOperationResult{}, domain.Invalid("unsupported operation upload field: " + field)
		}
		if _, duplicate := params[field]; duplicate {
			return ChatOperationResult{}, domain.Invalid("supply the upload only through files." + field)
		}
	}
	messageID := ""
	if op.MessageScoped {
		messageID, _ = params["message_id"].(string)
		if messageID == "" {
			return ChatOperationResult{}, domain.Invalid("message_id must be a string")
		}
		if _, err = s.Store.Message(ctx, chat.ID, messageID); err != nil {
			return ChatOperationResult{}, domain.Forbidden("message is not archived in the selected chat")
		}
	}
	uploads, err := s.operationFiles(ctx, b, request.Files)
	if err != nil {
		return ChatOperationResult{}, err
	}
	defer closeOperationFiles(uploads)
	if err = s.AuthorizeConversation(ctx, b.AgentID, chat.ID); err != nil {
		return ChatOperationResult{}, err
	}
	var raw json.RawMessage
	switch adapter := a.(type) {
	case *weixinAdapter:
		raw, err = adapter.operation(ctx, name, chat, params)
	case *feishu:
		raw, err = adapter.operation(ctx, name, chat, encoded(params))
	case *napcat:
		raw, err = napcatOperation(ctx, adapter, name, chat, params, uploads)
	}
	if err != nil {
		return ChatOperationResult{}, operationError(err)
	}
	if name == "delete_message" {
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		event := domain.Observation{SchemaVersion: 1, ConnectionID: accountID, ConversationID: chat.ID, Kind: "message.deleted", MessageID: messageID, ExternalEventID: domain.ID("delete"), SentAt: time.Now().UTC(), ReceivedAt: time.Now().UTC()}
		event.ID = stableID("event", accountID, event.ExternalEventID)
		if _, err = s.archiveEvent(persist, event); err != nil {
			return ChatOperationResult{Result: raw}, err
		}
	}
	return ChatOperationResult{Result: raw}, nil
}

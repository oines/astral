package channels

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/oines/astral/internal/domain"
)

// Named operations expose only chat-scoped protocol capabilities. The account
// selects the protocol; the caller cannot supply a URL or alternate destination.
type ChatOperation struct {
	Name          string   `json:"name"`
	Parameters    []string `json:"parameters"`
	MessageScoped bool     `json:"message_scoped"`
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
			result = append(result, ChatOperation{Name: op.Name, Parameters: op.Parameters, Documentation: op.Documentation})
		}
	case *feishu:
		for _, op := range FeishuOperationsCatalog() {
			result = append(result, ChatOperation{Name: op.Name, Parameters: op.Parameters, MessageScoped: op.MessageScoped, Documentation: op.Documentation})
		}
	case *napcat:
		for _, op := range []ChatOperation{
			{Name: "get_chat", Parameters: []string{}},
			{Name: "get_message", Parameters: []string{"message_id"}, MessageScoped: true},
			{Name: "delete_message", Parameters: []string{"message_id"}, MessageScoped: true},
			{Name: "get_members", Parameters: []string{}},
			{Name: "get_member", Parameters: []string{"user_id"}},
			{Name: "set_group_ban", Parameters: []string{"user_id", "duration"}},
		} {
			op.Documentation = "https://napneko.github.io/api/"
			result = append(result, op)
		}
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
		return ChatOperationResult{Result: result.Result}, err
	}
	if request.SourceConversationID != "" || len(request.Files) != 0 {
		return ChatOperationResult{}, domain.Invalid("this operation does not accept source chats or file uploads")
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
		raw, err = napcatOperation(ctx, adapter, name, chat, params)
	}
	if err != nil {
		return ChatOperationResult{}, err
	}
	if name == "delete_message" {
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		event := domain.Observation{SchemaVersion: 1, ConnectionID: accountID, ConversationID: chat.ID, Kind: "message.deleted", MessageID: messageID, ExternalEventID: domain.ID("delete"), SentAt: time.Now().UTC(), ReceivedAt: time.Now().UTC()}
		event.ID = stableID("event", accountID, event.ExternalEventID)
		if _, err = s.Store.Archive(persist, event); err != nil {
			return ChatOperationResult{Result: raw}, err
		}
	}
	return ChatOperationResult{Result: raw}, nil
}
func napcatOperation(ctx context.Context, n *napcat, name string, chat domain.Conversation, params map[string]any) (json.RawMessage, error) {
	kind, id, err := qqTarget(chat.ExternalID)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{}
	method := ""
	switch name {
	case "get_chat":
		if kind == "group" {
			method = "get_group_info"
			payload["group_id"] = id
		} else {
			method = "get_stranger_info"
			payload["user_id"] = id
		}
	case "get_message":
		method = "get_msg"
		payload["message_id"] = params["message_id"]
	case "delete_message":
		// NapCat's globally addressed short message IDs can outlive a mapping
		// reset. Verify the current platform mapping as well as the archived
		// authorization before a destructive request.
		var message json.RawMessage
		if err := n.call(ctx, "get_msg", map[string]any{"message_id": params["message_id"]}, &message); err != nil {
			return nil, err
		}
		if err := napcatMessageScope(ctx, n, chat, message, telegramID(params["message_id"])); err != nil {
			return nil, err
		}
		method = "delete_msg"
		payload["message_id"] = params["message_id"]
	case "get_members":
		method = "get_group_member_list"
		payload["group_id"] = id
	case "get_member", "set_group_ban":
		if number, ok := params["user_id"].(float64); ok && (number > 1<<53 || number < 1) {
			return nil, domain.Invalid("large user IDs must be strings")
		}
		userID := telegramID(params["user_id"])
		if !napcatNumericID(userID) {
			return nil, domain.Invalid("user_id must be a positive QQ number")
		}
		payload["group_id"] = id
		payload["user_id"] = userID
		if name == "get_member" {
			method = "get_group_member_info"
		} else {
			method = "set_group_ban"
			duration := telegramID(params["duration"])
			if duration == "" {
				duration = "1800"
			}
			// OneBot uses seconds, with zero lifting the restriction.
			var seconds int64
			if json.Unmarshal([]byte(duration), &seconds) != nil || seconds < 0 || seconds > 30*24*60*60 {
				return nil, domain.Invalid("duration must be between 0 and 2592000 seconds")
			}
			payload["duration"] = seconds
		}
	default:
		return nil, domain.Invalid("unknown NapCat chat operation")
	}
	if (name == "get_members" || name == "get_member" || name == "set_group_ban") && kind != "group" {
		return nil, domain.Invalid("this operation requires a group chat")
	}
	var result json.RawMessage
	if err = n.call(ctx, method, payload, &result); err != nil {
		if name == "delete_message" || name == "set_group_ban" {
			var api *napcatAPIError
			if errors.As(err, &api) {
				return nil, api.sendError()
			}
			return nil, &SendError{Message: err.Error(), Unknown: true}
		}
		return nil, err
	}
	if name == "get_message" {
		if err := napcatMessageScope(ctx, n, chat, result, telegramID(params["message_id"])); err != nil {
			return nil, err
		}
	}
	if len(result) == 0 {
		result = encoded(nil)
	}
	return result, nil
}

func napcatMessageScope(ctx context.Context, n *napcat, chat domain.Conversation, raw json.RawMessage, requested string) error {
	kind, id, err := qqTarget(chat.ExternalID)
	if err != nil {
		return err
	}
	var message struct {
		Type     string  `json:"message_type"`
		ID       qqValue `json:"message_id"`
		GroupID  qqValue `json:"group_id"`
		UserID   qqValue `json:"user_id"`
		TargetID qqValue `json:"target_id"`
	}
	if json.Unmarshal(raw, &message) != nil || message.ID == "" || string(message.ID) != requested || message.Type != kind {
		return domain.Forbidden("NapCat could not prove that this message belongs to the selected chat")
	}
	if kind == "group" {
		if string(message.GroupID) != id {
			return domain.Forbidden("NapCat message belongs to another group")
		}
		return nil
	}
	if string(message.UserID) == id && (message.TargetID == "" || string(message.TargetID) == n.connection.AccountID) {
		return nil
	}
	if string(message.UserID) != n.connection.AccountID {
		return domain.Forbidden("NapCat message belongs to another private chat")
	}
	if message.TargetID != "" {
		if string(message.TargetID) != id {
			return domain.Forbidden("NapCat message belongs to another private chat")
		}
		return nil
	}
	// NapCat get_msg omits the peer for self-sent private messages. The
	// explicitly chat-scoped history endpoint can prove recent membership;
	// an account-global message lookup alone cannot.
	var history struct {
		Messages []struct {
			ID qqValue `json:"message_id"`
		} `json:"messages"`
	}
	if err := n.call(ctx, "get_friend_msg_history", map[string]any{"user_id": id, "count": 100}, &history); err != nil {
		return err
	}
	for _, candidate := range history.Messages {
		if candidate.ID == message.ID {
			return nil
		}
	}
	return domain.Forbidden("NapCat could not find this self-sent message in the selected private chat's recent history")
}
func napcatNumericID(value string) bool {
	if len(value) == 0 || len(value) > 20 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != "0"
}

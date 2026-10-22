package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
)

type FeishuOperation struct {
	Name          string   `json:"name"`
	Parameters    []string `json:"parameters"`
	MessageScoped bool     `json:"message_scoped"`
	Documentation string   `json:"documentation"`
}

// This allowlist deliberately has no endpoint, receive_id or app credentials.
// Astral supplies the authorized conversation; global IDs alone grant nothing.
func FeishuOperationsCatalog() []FeishuOperation {
	return []FeishuOperation{
		{Name: "get_chat", Parameters: []string{}, Documentation: "https://open.feishu.cn/document/server-docs/group/chat/get"},
		{Name: "get_message", Parameters: []string{"message_id"}, MessageScoped: true, Documentation: "https://open.feishu.cn/document/server-docs/im-v1/message/get"},
		{Name: "delete_message", Parameters: []string{"message_id"}, MessageScoped: true, Documentation: "https://open.feishu.cn/document/server-docs/im-v1/message/delete"},
		{Name: "list_messages", Parameters: []string{"page_size", "page_token", "start_time", "end_time", "sort_type"}, Documentation: "https://open.feishu.cn/document/server-docs/im-v1/message/list"},
		{Name: "list_reactions", Parameters: []string{"message_id", "page_size", "page_token"}, MessageScoped: true, Documentation: "https://open.feishu.cn/document/server-docs/im-v1/message-reaction/list"},
		{Name: "add_reaction", Parameters: []string{"message_id", "emoji_type"}, MessageScoped: true, Documentation: "https://open.feishu.cn/document/server-docs/im-v1/message-reaction/create"},
		{Name: "delete_reaction", Parameters: []string{"message_id", "reaction_id"}, MessageScoped: true, Documentation: "https://open.feishu.cn/document/server-docs/im-v1/message-reaction/delete"},
	}
}

func (f *feishu) operation(ctx context.Context, name string, target domain.Conversation, body json.RawMessage) (json.RawMessage, error) {
	var operation *FeishuOperation
	for _, candidate := range FeishuOperationsCatalog() {
		if candidate.Name == name {
			operation = &candidate
			break
		}
	}
	if operation == nil {
		return nil, domain.Invalid("unknown Feishu chat operation")
	}
	if !feishuID(target.ExternalID) || target.TopicID != "" || target.ConnectionID != f.connection.ID {
		return nil, domain.Forbidden("Feishu operation requires the selected account chat")
	}
	if len(body) == 0 {
		body = json.RawMessage(`{}`)
	}
	var parameters map[string]json.RawMessage
	if json.Unmarshal(body, &parameters) != nil || parameters == nil {
		return nil, domain.Invalid("Feishu operation parameters must be an object")
	}
	for key := range parameters {
		allowed := false
		for _, field := range operation.Parameters {
			allowed = allowed || key == field
		}
		if !allowed {
			return nil, domain.Invalid("unsupported Feishu operation parameter: " + key)
		}
	}
	var values struct {
		MessageID  string `json:"message_id"`
		EmojiType  string `json:"emoji_type"`
		ReactionID string `json:"reaction_id"`
		PageSize   int    `json:"page_size"`
		PageToken  string `json:"page_token"`
		StartTime  string `json:"start_time"`
		EndTime    string `json:"end_time"`
		SortType   string `json:"sort_type"`
	}
	if json.Unmarshal(body, &values) != nil {
		return nil, domain.Invalid("invalid Feishu operation parameter type")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	messagePath := "/open-apis/im/v1/messages/" + url.PathEscape(values.MessageID)
	if operation.MessageScoped {
		message, err := f.getMessage(ctx, values.MessageID)
		if err != nil {
			return nil, err
		}
		if message.ChatID != target.ExternalID {
			return nil, domain.Forbidden("Feishu message belongs to another chat")
		}
		if name == "get_message" {
			return message.raw, nil
		}
	}
	method, path := http.MethodGet, ""
	var payload any
	query := url.Values{}
	if values.PageSize != 0 {
		if values.PageSize < 1 || values.PageSize > 50 {
			return nil, domain.Invalid("Feishu page_size must be between 1 and 50")
		}
		query.Set("page_size", strconv.Itoa(values.PageSize))
	}
	if len(values.PageToken) > 4096 {
		return nil, domain.Invalid("Feishu page_token is too long")
	}
	if values.PageToken != "" {
		query.Set("page_token", values.PageToken)
	}
	switch name {
	case "get_chat":
		path = "/open-apis/im/v1/chats/" + url.PathEscape(target.ExternalID)
	case "delete_message":
		method, path = http.MethodDelete, messagePath
	case "list_messages":
		path = "/open-apis/im/v1/messages"
		query.Set("container_id_type", "chat")
		query.Set("container_id", target.ExternalID)
		for key, value := range map[string]string{"start_time": values.StartTime, "end_time": values.EndTime} {
			if value != "" {
				if timestamp, err := strconv.ParseInt(value, 10, 64); err != nil || timestamp < 0 {
					return nil, domain.Invalid("Feishu history timestamps must be Unix seconds as strings")
				}
				query.Set(key, value)
			}
		}
		if values.SortType != "" {
			if values.SortType != "ByCreateTimeAsc" && values.SortType != "ByCreateTimeDesc" {
				return nil, domain.Invalid("Feishu sort_type must be ByCreateTimeAsc or ByCreateTimeDesc")
			}
			query.Set("sort_type", values.SortType)
		}
	case "list_reactions":
		path = messagePath + "/reactions"
	case "add_reaction":
		if !feishuID(values.EmojiType) || len(values.EmojiType) > 128 {
			return nil, domain.Invalid("Feishu emoji_type is required")
		}
		method, path = http.MethodPost, messagePath+"/reactions"
		payload = map[string]any{"reaction_type": map[string]string{"emoji_type": values.EmojiType}}
	case "delete_reaction":
		if len(values.ReactionID) > 512 || !feishuID(strings.NewReplacer("=", "", "+", "").Replace(values.ReactionID)) {
			return nil, domain.Invalid("Feishu reaction_id is required")
		}
		method, path = http.MethodDelete, messagePath+"/reactions/"+url.PathEscape(values.ReactionID)
	}
	if len(query) != 0 {
		path += "?" + query.Encode()
	}
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	if err := f.call(ctx, method, path, payload, &response); err != nil {
		if method != http.MethodGet {
			if _, known := err.(*feishuAPIError); !known {
				return nil, &SendError{Message: "Feishu operation outcome is unknown; inspect the message before retrying", Unknown: true}
			}
		}
		return nil, err
	}
	if len(bytes.TrimSpace(response.Data)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return response.Data, nil
}

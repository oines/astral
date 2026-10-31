package channels

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strconv"

	"github.com/oines/astral/internal/domain"
)

func telegramUserRead(method string) bool {
	return method == "getUserProfilePhotos" || method == "getUserProfileAudios" || method == "getUserPersonalChatMessages"
}

// telegramUserInteger accepts exact JSON integers and decimal strings, without
// allowing a rounded floating-point identifier to select a different user.
func telegramUserInteger(value any) (int64, bool) {
	const maxExact = 1<<53 - 1
	var number int64
	switch value := value.(type) {
	case string:
		if value == "" {
			return 0, false
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return 0, false
			}
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	case json.Number:
		return telegramUserInteger(string(value))
	case int:
		number = int64(value)
	case int64:
		number = value
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > maxExact || math.Trunc(value) != value {
			return 0, false
		}
		number = int64(value)
	default:
		return 0, false
	}
	return number, number >= 0 && number <= maxExact
}

func telegramUserParameters(operation TelegramOperation, target domain.Conversation, submitted map[string]any) (map[string]any, error) {
	parameters := make(map[string]any, len(submitted)+1)
	for key, value := range submitted {
		if !slices.Contains(operation.Parameters, key) {
			return nil, domain.Invalid("unsupported operation parameter: " + key)
		}
		parameters[key] = value
	}
	user, supplied := parameters["user_id"]
	if !supplied && target.Kind == "private" {
		user = target.ExternalID
	}
	userID, valid := telegramUserInteger(user)
	// Telegram User IDs have at most 52 significant bits.
	if !valid || userID == 0 || userID > 1<<52-1 {
		return nil, domain.Invalid("user_id must be a positive exact Telegram user identifier")
	}
	if target.Kind == "private" && strconv.FormatInt(userID, 10) != target.ExternalID {
		return nil, domain.Forbidden("user_id must identify the selected private chat's peer")
	}
	parameters["user_id"] = userID
	if offset, present := parameters["offset"]; present {
		value, valid := telegramUserInteger(offset)
		if !valid {
			return nil, domain.Invalid("offset must be a nonnegative exact integer")
		}
		parameters["offset"] = value
	}
	limit, present := parameters["limit"]
	if !present && operation.Name == "getUserPersonalChatMessages" {
		return nil, domain.Invalid("limit is required for getUserPersonalChatMessages")
	}
	if present {
		value, valid := telegramUserInteger(limit)
		maximum := int64(100)
		if operation.Name == "getUserPersonalChatMessages" {
			maximum = 20
		}
		if !valid || value == 0 || value > maximum {
			return nil, domain.Invalid("limit is outside the selected Telegram method's range")
		}
		parameters["limit"] = value
	}
	return parameters, nil
}

// User resources remain tied to an authorized chat. A private chat grants its
// peer; a parent group grants current members established by Telegram. Returned
// profile media and linked-chat messages are metadata, never inbound IM events.
func (s *Service) telegramUserOperation(ctx context.Context, binding domain.ExecutionBinding, accountID string, operation TelegramOperation, request TelegramRequest) (TelegramResult, error) {
	if request.SourceConversationID != "" || len(request.Files) != 0 {
		return TelegramResult{}, domain.Invalid("user profile reads do not accept source chats or file uploads")
	}
	adapter, _, err := s.telegramAccount(ctx, binding.AgentID, accountID)
	if err != nil {
		return TelegramResult{}, err
	}
	_, target, owner, err := s.target(ctx, binding.AgentID, binding.SessionID, request.ConversationID)
	if err != nil {
		return TelegramResult{}, err
	}
	if owner.ID != accountID {
		return TelegramResult{}, domain.Forbidden("chat belongs to another account")
	}
	if target.TopicID != "" {
		return TelegramResult{}, domain.Forbidden("user profile reads require access to the parent chat")
	}
	if target.Kind != "private" && target.Kind != "group" && target.Kind != "supergroup" {
		return TelegramResult{}, domain.Invalid("user profile reads require a private chat or a group")
	}
	parameters, err := telegramUserParameters(operation, target, request.Parameters)
	if err != nil {
		return TelegramResult{}, err
	}
	if target.Kind != "private" {
		var member struct {
			Status   string       `json:"status"`
			User     telegramUser `json:"user"`
			IsMember bool         `json:"is_member"`
		}
		if err := adapter.call(ctx, "getChatMember", map[string]any{"chat_id": target.ExternalID, "user_id": parameters["user_id"]}, &member); err != nil {
			return TelegramResult{}, err
		}
		current := member.Status == "creator" || member.Status == "administrator" || member.Status == "member" || member.Status == "restricted" && member.IsMember
		if !current || member.User.ID != parameters["user_id"].(int64) {
			return TelegramResult{}, domain.Forbidden("user is not a current member of the selected group")
		}
	}
	// Platform membership checks can take time; recheck local access before
	// reading the resource and again before returning it to the command.
	checkAccess := func() error {
		if _, _, err := s.telegramAccount(ctx, binding.AgentID, accountID); err != nil {
			return err
		}
		_, current, owner, err := s.target(ctx, binding.AgentID, binding.SessionID, target.ID)
		if err != nil {
			return err
		}
		if owner.ID != accountID || current.ExternalID != target.ExternalID || current.TopicID != target.TopicID {
			return domain.Conflict("selected chat changed during the user profile read")
		}
		return nil
	}
	if err := checkAccess(); err != nil {
		return TelegramResult{}, err
	}
	var result json.RawMessage
	if err := adapter.call(ctx, operation.Name, parameters, &result); err != nil {
		return TelegramResult{}, err
	}
	if err := checkAccess(); err != nil {
		return TelegramResult{}, err
	}
	return TelegramResult{Result: result}, nil
}

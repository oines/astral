package channels

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/oines/astral/internal/domain"
)

// WeixinOperationsCatalog describes the extra chat-scoped capability supported
// by the iLink client protocol. Message and file sends use the durable outbox.
func WeixinOperationsCatalog() []ChatOperation {
	return []ChatOperation{{Name: "set_typing", Parameters: []string{"active"}, Documentation: "https://github.com/Tencent/openclaw-weixin/blob/main/docs/protocol.md#sendtyping"}}
}

func (w *weixinAdapter) operation(ctx context.Context, name string, chat domain.Conversation, params map[string]any) (json.RawMessage, error) {
	if name != "set_typing" {
		return nil, domain.Invalid("unknown Weixin chat operation")
	}
	if len(params) != 1 {
		return nil, domain.Invalid("set_typing requires only active: true or false")
	}
	active, valid := params["active"].(bool)
	if !valid {
		return nil, domain.Invalid("set_typing requires active: true or false")
	}
	if chat.ConnectionID != w.connection.ID || chat.Kind != "private" || chat.TopicID != "" || chat.ExternalID == "" {
		return nil, domain.Invalid("Weixin typing requires a private chat on this account")
	}
	contextToken, err := w.getWire(ctx, "context", chat.ExternalID)
	if err != nil {
		return nil, err
	}
	if contextToken == "" {
		return nil, domain.Invalid("this Weixin user must message the Bot before typing can be requested")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var config struct {
		Ret    *int   `json:"ret"`
		Ticket string `json:"typing_ticket"`
	}
	if err := w.call(ctx, "getconfig", map[string]any{"ilink_user_id": chat.ExternalID, "context_token": contextToken}, &config); err != nil {
		return nil, err
	}
	if config.Ret == nil || config.Ticket == "" {
		return nil, domain.Offline("Weixin did not provide a typing ticket for this chat")
	}
	status := 2
	if active {
		status = 1
	}
	// The ticket is a transport secret. Neither it nor the context token leaves
	// this adapter in the Computer API result or error diagnostics.
	err = w.call(ctx, "sendtyping", map[string]any{"ilink_user_id": chat.ExternalID, "typing_ticket": config.Ticket, "status": status}, nil)
	if err != nil {
		var rejected *weixinAPIError
		if errors.As(err, &rejected) {
			return nil, &SendError{Message: rejected.Error()}
		}
		return nil, &SendError{Message: "Weixin typing request outcome is unknown", Unknown: true}
	}
	// The upstream client does not expose a delivery/state acknowledgement for
	// typing. Describe the successful request, without inventing a visible UI
	// state or claiming that the recipient has seen the indicator.
	return encoded(map[string]any{"requested": true, "active": active}), nil
}

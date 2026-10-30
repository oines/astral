package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/oines/astral/internal/domain"
)

// Resource reads share a single request budget, including all nested forwards.
// Attachment byte streams remain on the existing streaming download path.
func (r *messagePartResolver) napcatCall(ctx context.Context, n *napcat, action string, parameters any) (json.RawMessage, error) {
	if r.callsLeft <= 0 || r.bytesLeft <= 0 {
		return nil, domain.Invalid("message resource traversal exceeded its request budget")
	}
	r.callsLeft--
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, n.config.BaseURL+"/"+action, bytes.NewReader(encoded(parameters)))
	if err != nil {
		return nil, domain.Invalid("invalid NapCat message resource endpoint")
	}
	request.Header.Set("Content-Type", "application/json")
	if n.token != "" {
		request.Header.Set("Authorization", "Bearer "+n.token)
	}
	response, err := n.client.Do(request)
	if err != nil {
		return nil, domain.Offline("NapCat message resource could not be read")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, r.bytesLeft+1))
	r.bytesLeft -= int64(len(raw))
	if err != nil || r.bytesLeft < 0 {
		return nil, domain.Offline("NapCat message resource was interrupted or exceeded the size budget")
	}
	var envelope struct {
		Status string          `json:"status"`
		Code   int             `json:"retcode"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, domain.Offline("NapCat returned an invalid message resource")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Status != "ok" || envelope.Code != 0 {
		return nil, &napcatAPIError{Code: envelope.Code, Status: response.StatusCode}
	}
	return envelope.Data, nil
}

// Self-sent private messages can require an extra chat-scoped history proof.
// Count that proof against the same deadline, call and byte budgets too.
func (r *messagePartResolver) napcatScope(ctx context.Context, n *napcat, raw json.RawMessage, requested string) error {
	kind, peer, err := qqTarget(r.chat.ExternalID)
	if err != nil {
		return err
	}
	var message struct {
		Type     string  `json:"message_type"`
		ID       qqValue `json:"message_id"`
		UserID   qqValue `json:"user_id"`
		TargetID qqValue `json:"target_id"`
	}
	if json.Unmarshal(raw, &message) == nil && kind == "private" && message.Type == kind && string(message.ID) == requested && string(message.UserID) == n.connection.AccountID && message.TargetID == "" && peer != n.connection.AccountID {
		history, err := r.napcatCall(ctx, n, "get_friend_msg_history", map[string]any{"user_id": peer, "count": 100})
		if err != nil {
			return err
		}
		var page struct {
			Messages []struct {
				ID qqValue `json:"message_id"`
			} `json:"messages"`
		}
		if json.Unmarshal(history, &page) == nil {
			for _, candidate := range page.Messages {
				if candidate.ID == message.ID {
					return nil
				}
			}
		}
		return domain.Forbidden("NapCat could not prove this private message belongs to the selected chat")
	}
	return napcatMessageScope(ctx, n, r.chat, raw, requested)
}

func (r *messagePartResolver) forwardPage(ctx context.Context, part domain.MessagePart, offset, limit int) ([]domain.ForwardedMessage, int, error) {
	forward := part.Forward
	if forward == nil {
		return nil, 0, domain.Invalid("selected part is not a forward")
	}
	raw := forward.Raw
	if len(raw) == 0 && forward.ExternalID != "" {
		adapter, err := r.sourceAdapter(ctx)
		if err != nil {
			return nil, 0, err
		}
		n, ok := adapter.(*napcat)
		if !ok {
			return nil, 0, domain.Invalid("this account cannot expand forwarded messages")
		}
		// A platform message ID may be stale. Refresh and prove the enclosing
		// message's chat before using its current opaque forward handle.
		if !r.refreshed {
			if r.quoted {
				parent, err := r.napcatCall(ctx, n, "get_msg", map[string]any{"message_id": r.parentID})
				if err != nil {
					return nil, 0, err
				}
				if err = r.napcatScope(ctx, n, parent, r.parentID); err != nil {
					return nil, 0, err
				}
				var reply struct {
					Message []napcatSegment `json:"message"`
				}
				if json.Unmarshal(parent, &reply) != nil {
					return nil, 0, domain.Forbidden("NapCat could not verify the enclosing reply")
				}
				found := false
				for _, segment := range reply.Message {
					if segment.Type == "reply" && string(segment.Data["id"]) == r.messageID {
						found = true
						break
					}
				}
				if !found {
					return nil, 0, domain.Forbidden("current archived reply no longer references the selected source")
				}
			}
			source, err := r.napcatCall(ctx, n, "get_msg", map[string]any{"message_id": r.messageID})
			if err != nil {
				return nil, 0, err
			}
			if err = r.napcatScope(ctx, n, source, r.messageID); err != nil {
				return nil, 0, err
			}
			var message struct {
				Message json.RawMessage `json:"message"`
			}
			if json.Unmarshal(source, &message) != nil {
				return nil, 0, domain.Offline("NapCat returned invalid parent message parts")
			}
			r.current, _, _ = napcatParts(message.Message, r.messageID, "")
			r.refreshed = true
		}
		current, err := r.selectPart(ctx, r.current, part.Ref)
		if err != nil {
			return nil, 0, err
		}
		if current.Forward == nil {
			return nil, 0, domain.Forbidden("current parent no longer contains the selected forward")
		}
		if len(current.Forward.Raw) != 0 {
			raw = current.Forward.Raw
		} else {
			handle := current.Forward.ExternalID
			if handle == "" {
				return nil, 0, domain.Offline("current parent has no readable forward handle")
			}
			key := part.Ref + "\x00" + handle
			raw = r.forwards[key]
			if len(raw) == 0 {
				raw, err = r.napcatCall(ctx, n, "get_forward_msg", map[string]any{"message_id": handle})
				if err != nil {
					return nil, 0, err
				}
				r.forwards[key] = raw
			}
		}
	}
	if len(raw) != 0 {
		if int64(len(raw)) > 4<<20 {
			return nil, 0, domain.Invalid("inline forward exceeds the message resource size budget")
		}
		messages, total, err := napcatForwardPage(raw, r.messageID, part.Ref, offset, limit)
		if err != nil {
			return nil, 0, err
		}
		for _, message := range messages {
			if err := r.consumeParts(message.Parts, strings.Count(part.Ref, ".m")+1); err != nil {
				return nil, 0, err
			}
		}
		return messages, total, nil
	}
	if forward.Status == "truncated" || forward.Status == "unexpanded" || forward.Status == "unavailable" {
		return nil, 0, domain.Offline("forwarded message content is unavailable from its authorized parent")
	}
	total := len(forward.Messages)
	if offset >= total {
		return []domain.ForwardedMessage{}, total, nil
	}
	end := min(offset+limit, total)
	return forward.Messages[offset:end], total, nil
}

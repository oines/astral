package persistence

import (
	"context"
	"encoding/json"

	"github.com/oines/astral/internal/domain"
)

// MessageConversation resolves a message's archived source within one account.
// externalChatID is required for chat-local IDs (Telegram) and may be empty for
// account-wide IDs (Feishu). Reactions and other notices never prove membership.
func (s *Store) MessageConversation(ctx context.Context, connectionID, externalChatID, messageID string) (domain.Conversation, error) {
	var result domain.Conversation
	if connectionID == "" || messageID == "" {
		return result, domain.Missing("message source", messageID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.data FROM documents c
WHERE c.kind='conversations' AND json_extract(c.data,'$.connection_id')=?
AND (?='' OR json_extract(c.data,'$.external_id')=?)
AND EXISTS (SELECT 1 FROM observations o WHERE o.conversation_id=c.id
AND o.connection_id=? AND json_extract(o.payload,'$.message_id')=?
AND json_extract(o.payload,'$.kind') IN ('message.created','message.edited','message.deleted'))
ORDER BY c.id LIMIT 2`, connectionID, externalChatID, externalChatID, connectionID, messageID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		if found {
			return domain.Conversation{}, domain.Conflict("message has ambiguous archived chat membership")
		}
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return result, err
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return result, err
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if !found {
		return result, domain.Missing("message source", messageID)
	}
	return result, nil
}

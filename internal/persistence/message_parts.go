package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/oines/astral/internal/domain"
)

// MessagePartsSource prefers the original message, including its deletion
// tombstone. If it predates the archive, a current same-chat reply with retained
// source parts proves access to that quotation without inventing an original.
// quoted=true returns the enclosing reply, whose ReplyParts are the source.
func (s *Store) MessagePartsSource(ctx context.Context, conversationID, messageID string) (source domain.Observation, quoted bool, err error) {
	source, err = s.Message(ctx, conversationID, messageID)
	if err == nil {
		return source, false, nil
	}
	var failure *domain.Error
	if !errors.As(err, &failure) || failure.Status != 404 {
		return source, false, err
	}
	var raw []byte
	err = s.db.QueryRowContext(ctx, `SELECT o.payload FROM observations o
WHERE o.conversation_id=? AND json_extract(o.payload,'$.reply_id')=?
AND json_extract(o.payload,'$.kind') IN ('message.created','message.edited')
AND json_type(o.payload,'$.reply_parts')='array' AND json_array_length(o.payload,'$.reply_parts')>0
AND NOT EXISTS (SELECT 1 FROM observations newer WHERE newer.conversation_id=o.conversation_id AND newer.sequence>o.sequence
AND json_extract(newer.payload,'$.message_id')=json_extract(o.payload,'$.message_id')
AND json_extract(newer.payload,'$.kind') IN ('message.created','message.edited','message.deleted'))
ORDER BY o.sequence DESC LIMIT 1`, conversationID, messageID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return source, false, domain.Missing("message", messageID)
	}
	if err == nil {
		err = json.Unmarshal(raw, &source)
	}
	return source, true, err
}

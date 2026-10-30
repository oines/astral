package domain

import "time"

// ForwardOrigin describes the original source of a single forwarded message.
// It does not replace the enclosing message's sender, time or chat identity, and
// it is distinct from a merged-forward tree. Hidden users have no sender ID.
type ForwardOrigin struct {
	Type            string     `json:"type" enum:"user,hidden_user,chat,channel"`
	Sender          Sender     `json:"sender"`
	SentAt          *time.Time `json:"sent_at,omitempty"`
	MessageID       string     `json:"message_id,omitempty"`
	AuthorSignature string     `json:"author_signature,omitempty"`
}

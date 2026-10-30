package domain

import (
	"encoding/json"
	"time"
)

// MessagePart retains the ordered, quoted structure of an external message.
// Ref is a Host-assigned path (p0.m1.p2), never a platform message/chat ID.
// Only the enclosing Observation participates in routing and activation.
type MessagePart struct {
	Ref        string          `json:"ref"`
	Type       string          `json:"type" enum:"text,card,attachment,forward,unsupported"`
	Text       string          `json:"text,omitempty"`
	Card       *MessageCard    `json:"card,omitempty"`
	Attachment *Attachment     `json:"attachment,omitempty"`
	Forward    *MessageForward `json:"forward,omitempty"`
}

type MessageCard struct {
	Kind        string          `json:"kind"`
	Source      string          `json:"source,omitempty"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	URL         string          `json:"url,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

type MessageForward struct {
	ExternalID string             `json:"external_id,omitempty"`
	Messages   []ForwardedMessage `json:"messages,omitempty"`
	// Raw retains inline nodes omitted from the bounded normalized preview.
	// It belongs to the adapter; model rendering never emits this payload.
	Raw    json.RawMessage `json:"raw,omitempty"`
	Status string          `json:"status" enum:"inline,unexpanded,truncated,unavailable"`
	Total  int             `json:"total,omitempty"`
}

type ForwardedMessage struct {
	Sender Sender        `json:"sender"`
	SentAt *time.Time    `json:"sent_at,omitempty"`
	Parts  []MessagePart `json:"parts"`
}

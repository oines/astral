// Package wake owns external event endpoints and bounded, durable event batching.
package wake

import (
	"context"
	"encoding/json"
	"time"
)

const Kind = "wake_endpoints"
const MaxRequestBytes = 64 << 10
const MaxEventBytes = 1 << 10
const MaxBatchBytes = 4 << 10
const MaxBatchEvents = 20
const MergeWindow = time.Second
const BlockDuration = time.Minute

type CreateInput struct {
	Title     string `json:"title"`
	SessionID string `json:"session_id,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
	Notes     string `json:"notes,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}
type Patch struct {
	Revision int64   `json:"revision"`
	Title    *string `json:"title,omitempty"`
	Prompt   *string `json:"prompt,omitempty"`
	Notes    *string `json:"notes,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
}
type Event struct {
	EventID string          `json:"event_id"`
	Text    string          `json:"text,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}
type Receipt struct {
	EventID   string `json:"event_id"`
	ReceiptID string `json:"receipt_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Truncated bool   `json:"truncated"`
}
type BatchEvent struct {
	EndpointID   string    `json:"endpoint_id"`
	ComputerID   string    `json:"computer_id"`
	ComputerName string    `json:"computer_name"`
	EventID      string    `json:"event_id"`
	Title        string    `json:"title"`
	Prompt       string    `json:"prompt,omitempty"`
	Notes        string    `json:"notes,omitempty"`
	Text         string    `json:"text"`
	ReceivedAt   time.Time `json:"received_at"`
	Truncated    bool      `json:"truncated,omitempty"`
}
type Batch struct {
	ID         string       `json:"id"`
	Digest     string       `json:"digest"`
	AgentID    string       `json:"agent_id"`
	SessionID  string       `json:"session_id"`
	Events     []BatchEvent `json:"events,omitempty"`
	Flood      bool         `json:"flood,omitempty"`
	Time       time.Time    `json:"time"`
	Generation uint64       `json:"generation"`
}
type DeliveryResult struct {
	Accepted bool
	Handled  bool
}
type Deliver func(context.Context, Batch) (DeliveryResult, error)

package domain

import "time"

// WakeEndpoint is a Bot-owned external event entry point targeting a root Session.
// TriggerURL is supplied at the HTTP presentation boundary, never an identity grant.
type WakeEndpoint struct {
	Meta
	AgentID         string     `json:"agent_id"`
	ComputerID      string     `json:"computer_id"`
	SessionID       string     `json:"session_id"`
	SourceSessionID string     `json:"source_session_id"`
	Title           string     `json:"title"`
	Prompt          string     `json:"prompt,omitempty"`
	Notes           string     `json:"notes,omitempty"`
	Enabled         bool       `json:"enabled"`
	TriggerURL      string     `json:"trigger_url,omitempty"`
	BlockedUntil    *time.Time `json:"blocked_until,omitempty"`
	LastReceivedAt  *time.Time `json:"last_received_at,omitempty"`
	LastStatus      string     `json:"last_status,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
}

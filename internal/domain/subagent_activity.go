package domain

import "time"

// SubagentActivity is a UI fact sourced from a child's durable Run entry. It
// is not model input and does not describe the business outcome of its task.
type SubagentActivity struct {
	ID             string    `json:"id"`
	SessionID      string    `json:"session_id"`
	ChildSessionID string    `json:"child_session_id"`
	Name           string    `json:"name"`
	RunID          string    `json:"run_id"`
	Status         string    `json:"status"`
	Time           time.Time `json:"time"`
	Sequence       uint64    `json:"sequence"`
}

type SubagentActivityPage struct {
	Activities []SubagentActivity `json:"activities"`
	Cursor     string             `json:"cursor"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

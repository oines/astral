package domain

import "time"

// Schedule is a Host-owned instruction to deliver to an existing or new Session.
// LastStatus describes delivery, never completion of the Agent's work.
type Schedule struct {
	Meta
	AgentID         string         `json:"agent_id"`
	SessionMode     string         `json:"session_mode" enum:"existing,new"`
	SessionID       string         `json:"session_id,omitempty"`
	SourceSessionID string         `json:"source_session_id,omitempty"`
	LastSessionID   string         `json:"last_session_id,omitempty"`
	Title           string         `json:"title"`
	Prompt          string         `json:"prompt"`
	Enabled         bool           `json:"enabled"`
	Timing          ScheduleTiming `json:"timing"`
	NextRunAt       *time.Time     `json:"next_run_at,omitempty"`
	LastRunAt       *time.Time     `json:"last_run_at,omitempty"`
	LastStatus      string         `json:"last_status,omitempty" enum:"dispatching,delivered,missed,failed"`
	LastError       string         `json:"last_error,omitempty"`
}

type ScheduleTiming struct {
	Kind string     `json:"kind" enum:"once,cron"`
	At   *time.Time `json:"at,omitempty"`
	Cron string     `json:"cron,omitempty"`
}

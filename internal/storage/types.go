// Package storage measures Host-owned files and estimates logical history through
// public Core APIs. Logical estimates are not SQLite allocation or freed bytes.
package storage

import "time"

type PhysicalItem struct {
	ID    string `json:"id"`
	Bytes int64  `json:"bytes"`
}
type Physical struct {
	TotalBytes     int64          `json:"total_bytes"`
	DatabaseBytes  int64          `json:"database_bytes"`
	BackupBytes    int64          `json:"backup_bytes"`
	CacheBytes     int64          `json:"cache_bytes"`
	LogBytes       int64          `json:"log_bytes"`
	TemporaryBytes int64          `json:"temporary_bytes"`
	DiskTotalBytes int64          `json:"disk_total_bytes"`
	DiskUsedBytes  int64          `json:"disk_used_bytes"`
	AvailableBytes int64          `json:"available_bytes"`
	Items          []PhysicalItem `json:"items"`
	Errors         []string       `json:"errors"`
}
type Estimate struct {
	State     string     `json:"state"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}
type Chat struct {
	ConversationID  string `json:"conversation_id"`
	ConnectionID    string `json:"connection_id"`
	AccountName     string `json:"account_name"`
	Title           string `json:"title"`
	Platform        string `json:"platform"`
	BindingID       string `json:"binding_id"`
	BindingRevision int64  `json:"binding_revision"`
	SessionID       string `json:"session_id"`
	SessionRevision int64  `json:"session_revision"`
	RetentionDays   int    `json:"retention_days"`
	ArchiveCount    int64  `json:"archive_count"`
	ArchiveOldest   string `json:"archive_oldest,omitempty"`
	ArchiveNewest   string `json:"archive_newest,omitempty"`
	LogicalBytes    int64  `json:"logical_bytes"`
	IMBytes         int64  `json:"im_bytes"`
	RolloutBytes    int64  `json:"rollout_bytes"`
	EstimateState   string `json:"estimate_state"`
	Error           string `json:"error,omitempty"`
}
type Session struct {
	SessionID     string `json:"session_id"`
	Title         string `json:"title"`
	Revision      int64  `json:"revision"`
	Archived      bool   `json:"archived"`
	LogicalBytes  int64  `json:"logical_bytes"`
	EstimateState string `json:"estimate_state"`
	Error         string `json:"error,omitempty"`
}
type Agent struct {
	AgentID      string    `json:"agent_id"`
	Name         string    `json:"name"`
	LogicalBytes int64     `json:"logical_bytes"`
	Chats        []Chat    `json:"chats"`
	Sessions     []Session `json:"sessions"`
}
type Reclaim struct {
	State       string     `json:"state"`
	RequestedAt *time.Time `json:"requested_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	Error       string     `json:"error,omitempty"`
}
type Snapshot struct {
	Directory string   `json:"directory"`
	Physical  Physical `json:"physical"`
	Estimate  Estimate `json:"estimate"`
	Agents    []Agent  `json:"agents"`
	Reclaim   Reclaim  `json:"reclaim"`
}
type RefreshRequest struct {
	Cancel bool `json:"cancel,omitempty"`
}
type CleanupItem struct {
	Token     string `json:"token"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Bytes     int64  `json:"bytes"`
	Protected bool   `json:"protected"`
	Reason    string `json:"reason,omitempty"`
}
type CleanupInventory struct {
	Items      []CleanupItem `json:"items"`
	TotalBytes int64         `json:"total_bytes"`
	Error      string        `json:"error,omitempty"`
}
type CleanupRequest struct {
	OperationID string   `json:"operation_id"`
	Tokens      []string `json:"tokens"`
}
type CleanupOperation struct {
	OperationID   string   `json:"operation_id"`
	State         string   `json:"state"`
	RemovedBytes  int64    `json:"removed_bytes"`
	RemovedTokens []string `json:"removed_tokens"`
	Error         string   `json:"error,omitempty"`
}

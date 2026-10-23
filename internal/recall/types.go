// Package recall provides read-only, Agent-scoped access to durable conversation
// history. Its SQLite database is a disposable text index, never a source of
// execution state. No operation opens a model or a Session runtime.
package recall

import "time"

type Session struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	Source          string    `json:"source"`
	ConversationID  string    `json:"conversation_id,omitempty"`
	ChannelPlatform string    `json:"channel_platform,omitempty"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	Archived        bool      `json:"archived"`
	CreatedAt       time.Time `json:"created_at"`
	LastActivityAt  time.Time `json:"last_activity_at"`
	HeadID          string    `json:"head_id,omitempty"`
}

type ListRequest struct {
	Cursor         string
	Limit          int
	Source         string
	ConversationID string
	Archived       *bool
	Title          string
}

type SessionPage struct {
	Items      []Session `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type SearchRequest struct {
	Query          string     `json:"query,omitempty" description:"Plain text, not an FTS expression. Required on the first page. Words are ANDed by default; phrase treats the entire query literally."`
	Match          string     `json:"match,omitempty" enum:"all,any,phrase" description:"Default all. all/any split on Unicode whitespace and match within one text part, not across a whole Session."`
	CaseSensitive  bool       `json:"case_sensitive,omitempty"`
	SessionIDs     []string   `json:"session_ids,omitempty" description:"Omit to search all Sessions owned by the current Agent, including archived Sessions and inherited Fork history."`
	Source         string     `json:"source,omitempty"`
	ConversationID string     `json:"conversation_id,omitempty"`
	Archived       *bool      `json:"archived,omitempty"`
	Roles          []string   `json:"roles,omitempty" description:"Optional user, assistant or tool role filter."`
	Kinds          []string   `json:"kinds,omitempty" description:"message, observation, tool_call, tool_result, attachment, summary. Default excludes summary; original pre-compaction history remains searchable."`
	After          *time.Time `json:"after,omitempty" description:"Inclusive original entry time, RFC3339."`
	Before         *time.Time `json:"before,omitempty" description:"Exclusive original entry time, RFC3339."`
	Order          string     `json:"order,omitempty" enum:"newest,oldest" description:"Default newest. Within each text part, passages are in character-offset order."`
	ContextChars   int        `json:"context_chars,omitempty" description:"Target passage width in Unicode code points, including the match. Default 600; 32–4096. No per-Session quota or overall model-context budget is applied."`
	Limit          int        `json:"limit,omitempty" description:"Passages per page, default 50; 1–200. All remaining matches can be paged through."`
	Cursor         string     `json:"cursor,omitempty" description:"Resume a fixed search snapshot with cursor and optional limit only. Cursors expire after 24 hours; source references do not."`
}

type SearchPage struct {
	Hits       []Hit  `json:"hits"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Offsets always refer to the returned text part, in Unicode code points.
// Ref is an address into immutable source history, not an index row ID.
type Excerpt struct {
	EntryID         string    `json:"entry_id"`
	SourceSessionID string    `json:"source_session_id"`
	Part            int       `json:"part"`
	Kind            string    `json:"kind"`
	Role            string    `json:"role"`
	ToolName        string    `json:"tool_name,omitempty"`
	Time            time.Time `json:"time"`
	Text            string    `json:"text"`
	Start           int       `json:"start"`
	End             int       `json:"end"`
	TotalChars      int       `json:"total_chars"`
	Ref             string    `json:"ref"`
	ReadURL         string    `json:"read_url"`
}

type Hit struct {
	SessionID    string `json:"session_id"`
	SessionTitle string `json:"session_title"`
	Excerpt
	MatchCount int `json:"match_count"`
}

type ReadRequest struct {
	Cursor string
	Around string
	Limit  int
	Before *int
	After  *int
	Offset *int
	Length int
}

type HistoryPage struct {
	SessionID   string    `json:"session_id"`
	HeadID      string    `json:"head_id"`
	Items       []Excerpt `json:"items"`
	OlderCursor string    `json:"older_cursor,omitempty"`
	NewerCursor string    `json:"newer_cursor,omitempty"`
}

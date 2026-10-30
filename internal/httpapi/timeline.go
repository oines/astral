package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/gateway"
)

// timelineEntry owns its presentation slices. Already-loaded execution details
// are complete: expanding them requires no second network request. Provider
// protocol data and image bytes remain outside the transcript response.
func timelineEntry(entry core.Entry) core.Entry {
	entry.Created = nil
	entry.Configuration = nil
	entry.Input = nil
	entry.Compaction = nil
	entry.Approval = nil
	if entry.Message != nil {
		message := *entry.Message
		var metadata map[string]json.RawMessage
		_ = json.Unmarshal(message.Metadata, &metadata)
		var source string
		_ = json.Unmarshal(metadata["source"], &source)
		if entry.Kind != core.EntryMessage || message.ContextKind != "" || source == "im.trigger" {
			entry.Message = nil
		} else {
			message.Content = timelineContent(message.Content)
			visibleMetadata := map[string]json.RawMessage{}
			for _, key := range []string{"source", "display_text", "files", "sender", "schedule", "accepted_at"} {
				if value, ok := metadata[key]; ok {
					visibleMetadata[key] = value
				}
			}
			message.Metadata = nil
			if len(visibleMetadata) > 0 {
				message.Metadata, _ = json.Marshal(visibleMetadata)
			}
			if _, ok := metadata["display_text"]; ok && message.Role == core.RoleUser {
				for i := range message.Content {
					if message.Content[i].Type == "text" {
						message.Content[i].Text = ""
					}
				}
			}
			entry.Message = &message
		}
	}
	if entry.Tool != nil {
		tool := *entry.Tool
		tool.Invocation.Binding = nil
		tool.Invocation.Call.Metadata = nil
		// Arguments and Host/tool-owned Details are actual execution UI data,
		// including output, patch, search results and exit status. Do not truncate.
		if tool.Result != nil {
			result := *tool.Result
			result.Content = timelineContent(result.Content)
			tool.Result = &result
		}
		entry.Tool = &tool
	}
	return entry
}

func timelineContent(content []core.Content) []core.Content {
	out := append([]core.Content(nil), content...)
	for i := range out {
		part := &out[i]
		part.Data = nil
		metadata := part.Metadata
		part.Metadata = nil
		if part.Type == "image" {
			var dimensions struct {
				Width  int `json:"width,omitempty"`
				Height int `json:"height,omitempty"`
			}
			if json.Unmarshal(metadata, &dimensions) == nil && (dimensions.Width > 0 || dimensions.Height > 0) {
				part.Metadata, _ = json.Marshal(dimensions)
			}
			if strings.HasPrefix(part.URL, "data:") {
				part.URL = ""
			}
		}
		if part.ToolCall != nil {
			call := *part.ToolCall
			call.Metadata = nil
			part.ToolCall = &call
		}
	}
	return out
}

// A visible unit is a message or a tool execution, not an internal input,
// environment or configuration event. Run facts are retained for terminal state
// reconciliation without consuming the visible-content budget.
func timelineUnit(entry core.Entry) string {
	if entry.Tool != nil {
		id := entry.Tool.Invocation.ExecutionID
		if id == "" {
			id = entry.ID
		}
		return "tool:" + id
	}
	if entry.Message != nil {
		message := entry.Message
		if message.IsError || message.Failed() {
			return ""
		}
		var metadata map[string]json.RawMessage
		_ = json.Unmarshal(message.Metadata, &metadata)
		var display string
		_ = json.Unmarshal(metadata["display_text"], &display)
		var files []json.RawMessage
		_ = json.Unmarshal(metadata["files"], &files)
		if display != "" || len(files) > 0 {
			return "message:" + entry.ID
		}
		for _, part := range message.Content {
			if part.Type == "image" || part.Text != "" || part.ToolCall != nil {
				return "message:" + entry.ID
			}
		}
	}
	return ""
}

func timelineRelevant(entry core.Entry) bool { return timelineUnit(entry) != "" || entry.Run != nil }

const timelinePageBytes = 8 << 20
const timelineScanBytes = 64 << 20
const timelineScanEntries = 1024
const timelineScanPage = 64

// timelineHistory follows exactly the Store's branch path / current-writer trace
// semantics. Presentation filtering happens while consuming raw pages, so an
// invisible context backlog does not turn one screen into many network trips.
func timelineHistory(ctx context.Context, store core.Store, id, cursor string, limit int, trace bool) (gateway.HistoryPage, error) {
	state, err := store.LoadState(ctx, id)
	if err != nil {
		return gateway.HistoryPage{}, err
	}
	head := state.Branch.HeadID
	result := gateway.HistoryPage{Entries: []core.Entry{}, HeadID: head}
	if !trace && head == "" {
		return result, nil
	}
	limit = max(1, min(limit, 200))
	query := core.Query{Cursor: cursor, Limit: timelineScanPage, MaxBytes: 8 << 20}
	if trace {
		query.SessionID = id
		query.Reverse = true
	} else {
		query.PathHead = head
		query.HistoryOnly = true
	}
	units := map[string]bool{}
	scanned, rawBytes, responseBytes := 0, 0, 0
	for {
		page, err := store.Scan(ctx, state.RolloutID, query)
		if errors.Is(err, core.ErrBudget) {
			// Preserve a complete single large record, matching the old history API's
			// maximum record budget. Page bytes are a soft ceiling for this one case.
			large := query
			large.Limit = 1
			large.MaxBytes = 128 << 20
			page, err = store.Scan(ctx, state.RolloutID, large)
		}
		if err != nil {
			return gateway.HistoryPage{}, err
		}
		progress := query.Cursor
		for index, record := range page.Records {
			entry := timelineEntry(record.Entry)
			unit := timelineUnit(entry)
			include := timelineRelevant(entry) && (unit == "" || !units[unit])
			var encoded []byte
			if include {
				encoded, err = json.Marshal(entry)
				if err != nil {
					return gateway.HistoryPage{}, err
				}
				if len(result.Entries) > 0 && responseBytes+len(encoded) > timelinePageBytes {
					// No record at or beyond progress has been consumed. The next request
					// starts with this entire record; never cut text to fit the page.
					result.NextCursor = progress
					return result, nil
				}
			}
			scanned++
			raw, err := json.Marshal(record)
			if err != nil {
				return gateway.HistoryPage{}, err
			}
			rawBytes += len(raw)
			if trace {
				progress = strconv.FormatUint(record.Entry.Sequence, 10)
			} else {
				progress = record.Entry.ParentID
			}
			if include {
				result.Entries = append(result.Entries, entry)
				responseBytes += len(encoded)
				if unit != "" {
					units[unit] = true
				}
			}
			if len(units) >= limit || scanned >= timelineScanEntries || rawBytes >= timelineScanBytes || responseBytes >= timelinePageBytes {
				if index < len(page.Records)-1 || page.Next != "" {
					result.NextCursor = progress
				}
				return result, nil
			}
		}
		if page.Next == "" {
			return result, nil
		}
		if len(page.Records) == 0 || page.Next == query.Cursor {
			return gateway.HistoryPage{}, core.ErrCorrupt
		}
		query.Cursor = page.Next
	}
}

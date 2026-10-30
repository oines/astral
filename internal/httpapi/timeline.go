package httpapi

import (
	"encoding/json"

	core "github.com/oines/astral-core"

	"github.com/oines/astral/internal/gateway"
)

// timelineEntry is a presentation copy, never a new persisted history format.
// Complete tool results remain available from the authenticated entry endpoint.
func timelineEntry(entry core.Entry) core.Entry {
	entry.Created = nil
	entry.Configuration = nil
	entry.Input = nil
	entry.Compaction = nil
	entry.Approval = nil
	if entry.Message != nil {
		message := *entry.Message
		if entry.Kind != core.EntryMessage || message.ContextKind != "" {
			entry.Message = nil
		} else {
			var metadata map[string]json.RawMessage
			_ = json.Unmarshal(message.Metadata, &metadata)
			var source string
			_ = json.Unmarshal(metadata["source"], &source)
			if source == "im.trigger" {
				entry.Message = nil
			} else {
				message.Content = append([]core.Content(nil), message.Content...)
				for i := range message.Content {
					part := &message.Content[i]
					part.Data = nil
					if part.Type != "image" {
						part.Metadata = nil
					}
					if part.ToolCall != nil {
						call := *part.ToolCall
						call.Arguments = nil
						part.ToolCall = &call
					}
				}
				if _, ok := metadata["display_text"]; ok && message.Role == core.RoleUser {
					// The model envelope can dwarf its user-facing presentation.
					for i := range message.Content {
						if message.Content[i].Type == "text" {
							message.Content[i].Text = ""
						}
					}
				}
				entry.Message = &message
			}
		}
	}
	if entry.Tool != nil {
		tool := *entry.Tool
		tool.Decision = nil
		var args map[string]json.RawMessage
		_ = json.Unmarshal(tool.Invocation.Call.Arguments, &args)
		preview := map[string]string{}
		for _, key := range []string{"command", "cmd", "path", "query", "url"} {
			var text string
			if json.Unmarshal(args[key], &text) == nil && text != "" {
				runes := []rune(text)
				if len(runes) > 512 {
					text = string(runes[:512]) + "…"
				}
				preview[key] = text
			}
		}
		tool.Invocation.Call.Arguments, _ = json.Marshal(preview)
		if tool.Result != nil {
			result := *tool.Result
			result.Content = nil
			result.Details = nil
			tool.Result = &result
		}
		entry.Tool = &tool
	}
	return entry
}

func timelinePage(page gateway.HistoryPage) gateway.HistoryPage {
	entries := make([]core.Entry, len(page.Entries))
	for i, entry := range page.Entries {
		entries[i] = timelineEntry(entry)
	}
	page.Entries = entries
	return page
}
